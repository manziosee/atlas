package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

// Write forwarding: a service is owned by the node that registered it, and
// only the owner changes it. When a write for another node's service arrives,
// this node sends the same request to the owner's HTTP API (learned through
// gossip) with the caller's own bearer token, and relays the answer. The
// owner authorizes the request again, so forwarding grants no extra access.

// ForwardedByHeader marks a forwarded request. A node never forwards a request
// that carries it, which rules out loops; spoofing it only disables forwarding.
const ForwardedByHeader = "X-Atlas-Forwarded-By"

const maxForwardResponse = 1 << 20

type forwardedKey struct{}

type clientAddrKey struct{}

// withRequestOrigin records whether a request was forwarded and the client
// chain to pass on as X-Forwarded-For.
func withRequestOrigin(ctx context.Context, forwarded bool, clientChain string) context.Context {
	return context.WithValue(context.WithValue(ctx, forwardedKey{}, forwarded), clientAddrKey{}, clientChain)
}

func isForwarded(ctx context.Context) bool {
	forwarded, _ := ctx.Value(forwardedKey{}).(bool)
	return forwarded
}

// clientChain returns r's X-Forwarded-For value extended with its peer.
func clientChain(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if prior := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); prior != "" {
		return prior + ", " + host
	}
	return host
}

// originMiddleware tags /v1 requests with their forwarding origin.
func originMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withRequestOrigin(r.Context(), r.Header.Get(ForwardedByHeader) != "", clientChain(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// remoteError is an owner's error response that has no local equivalent; it
// is relayed with the owner's status and code.
type remoteError struct {
	status int
	body   httperr.Body
}

func (e *remoteError) Error() string {
	return fmt.Sprintf("owner returned %d %s: %s", e.status, e.body.Code, e.body.Error)
}

// ownerUnreachableError means the owner is alive in gossip but its API did
// not answer.
type ownerUnreachableError struct {
	owner string
	err   error
}

func (e *ownerUnreachableError) Error() string {
	return "could not reach owner node " + e.owner + ": " + e.err.Error()
}

func (e *ownerUnreachableError) Unwrap() error { return e.err }

// forwardService sends an owner-only write to owner and decodes the service
// it returns. ok is false when forwarding is not possible: it is disabled,
// the request was already forwarded, the owner is not a live member with an
// API address, or the request carried no bearer token. The caller then
// reports its local error. status is the owner's HTTP status.
func (d Deps) forwardService(ctx context.Context, owner, method, path string, body any, ifRevision uint64) (storage.Service, int, bool, error) {
	if d.Forwarder == nil || isForwarded(ctx) {
		return storage.Service{}, 0, false, nil
	}
	member, alive := d.member(owner)
	token := security.BearerToken(ctx)
	if !alive || member.APIAddr == "" || owner == d.Cluster.LocalNode().ID || token == "" {
		return storage.Service{}, 0, false, nil
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return storage.Service{}, 0, true, err
		}
		payload = bytes.NewReader(encoded)
	}
	// The target is a cluster member's API address from authenticated gossip.
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(member.APIAddr, "/")+path, payload) // #nosec G704 -- see above.
	if err != nil {
		return storage.Service{}, 0, true, &ownerUnreachableError{owner: owner, err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(ForwardedByHeader, d.Cluster.LocalNode().ID)
	if chain, _ := ctx.Value(clientAddrKey{}).(string); chain != "" {
		req.Header.Set("X-Forwarded-For", chain)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifRevision != 0 {
		req.Header.Set("If-Match", strconv.Quote(strconv.FormatUint(ifRevision, 10)))
	}
	resp, err := d.Forwarder.Do(req) // #nosec G704 -- the target is a cluster member's gossiped API address.
	if err != nil {
		return storage.Service{}, 0, true, &ownerUnreachableError{owner: owner, err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxForwardResponse))
	if err != nil {
		return storage.Service{}, resp.StatusCode, true, &ownerUnreachableError{owner: owner, err: err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var service storage.Service
		if resp.StatusCode != http.StatusNoContent && len(raw) > 0 {
			if err := json.Unmarshal(raw, &service); err != nil {
				return storage.Service{}, resp.StatusCode, true, fmt.Errorf("decode owner response: %w", err)
			}
		}
		return service, resp.StatusCode, true, nil
	}
	return storage.Service{}, resp.StatusCode, true, ownerError(resp.StatusCode, raw, owner)
}

// ownerError converts the owner's JSON error into the error the local
// operation would have returned, so both transports report it the same way.
func ownerError(status int, raw []byte, owner string) error {
	var body httperr.Body
	if json.Unmarshal(raw, &body) != nil || body.Code == "" {
		return &remoteError{status: status, body: httperr.Body{Error: strings.TrimSpace(string(raw)), Code: httperr.CodeInternal}}
	}
	switch body.Code {
	case httperr.CodeNotFound:
		return storage.ErrNotFound
	case httperr.CodePreconditionFailed:
		return storage.ErrRevisionMismatch
	case httperr.CodeNoTTL:
		return storage.ErrNoTTL
	case httperr.CodeValidation, httperr.CodeInvalidJSON:
		return validationError{msg: body.Error}
	case httperr.CodeNotOwner, httperr.CodeOwnerAlive:
		if body.OwnerNode == "" {
			body.OwnerNode = owner
		}
		return &storage.NotOwnerError{Owner: body.OwnerNode}
	default:
		return &remoteError{status: status, body: body}
	}
}
