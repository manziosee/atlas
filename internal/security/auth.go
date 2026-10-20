package security

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

type Scope string

const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
	ScopeAdmin Scope = "admin"
)

// Credential is a scoped bearer credential. Plaintext exists only in config
// and the caller's request; the authenticator retains a digest only.
type Credential struct {
	Token     string    `json:"token"`
	Scopes    []Scope   `json:"scopes"`
	ExpiresAt time.Time `json:"expires_at"`
}

type credentialRecord struct {
	digest    [sha256.Size]byte
	Scopes    []Scope
	expiresAt time.Time
}

// TokenAuthenticator stores SHA-256 digests and applies credential scopes and
// expiry on every request. Legacy ATLAS_API_TOKENS entries remain admin
// credentials without an expiry; new deployments should use scoped entries.
type TokenAuthenticator struct {
	mu          sync.RWMutex
	credentials []credentialRecord
}

type tokenIDKey struct{}

func NewTokenAuthenticator(tokens []string) *TokenAuthenticator {
	a := &TokenAuthenticator{}
	_ = a.ReplaceConfigured(tokens, nil)
	return a
}

func NewCredentialAuthenticator(credentials []Credential) (*TokenAuthenticator, error) {
	a := &TokenAuthenticator{}
	if err := a.ReplaceConfigured(nil, credentials); err != nil {
		return nil, err
	}
	return a, nil
}

func NewConfiguredTokenAuthenticator(tokens []string, credentials []Credential) (*TokenAuthenticator, error) {
	a := &TokenAuthenticator{}
	if err := a.ReplaceConfigured(tokens, credentials); err != nil {
		return nil, err
	}
	return a, nil
}

func ValidateCredentials(credentials []Credential) error {
	seen := make(map[[sha256.Size]byte]struct{}, len(credentials))
	for _, credential := range credentials {
		if len(credential.Token) < 32 {
			return errors.New("security: credential token must be at least 32 characters")
		}
		if len(credential.Scopes) == 0 {
			return errors.New("security: credential must include at least one scope")
		}
		for _, scope := range credential.Scopes {
			if scope != ScopeRead && scope != ScopeWrite && scope != ScopeAdmin {
				return errors.New("security: credential scope must be read, write, or admin")
			}
		}
		seenScopes := make(map[Scope]struct{}, len(credential.Scopes))
		for _, scope := range credential.Scopes {
			if _, exists := seenScopes[scope]; exists {
				return errors.New("security: duplicate credential scope")
			}
			seenScopes[scope] = struct{}{}
		}
		if credential.ExpiresAt.IsZero() || !credential.ExpiresAt.After(time.Now()) {
			return errors.New("security: credential expiry is required and must be in the future")
		}
		digest := sha256.Sum256([]byte(credential.Token))
		if _, ok := seen[digest]; ok {
			return errors.New("security: duplicate credential token")
		}
		seen[digest] = struct{}{}
	}
	return nil
}

func (a *TokenAuthenticator) Replace(tokens []string) {
	_ = a.ReplaceConfigured(tokens, nil)
}

// ReplaceConfigured atomically swaps static compatibility tokens and scoped
// credentials, retaining only digests. Supplying both forms is ambiguous.
func (a *TokenAuthenticator) ReplaceConfigured(tokens []string, credentials []Credential) error {
	if len(tokens) > 0 && len(credentials) > 0 {
		return errors.New("security: configure either static API tokens or scoped credentials, not both")
	}
	if err := ValidateCredentials(credentials); err != nil {
		return err
	}
	records := make([]credentialRecord, 0, len(tokens)+len(credentials))
	for _, token := range tokens {
		records = append(records, credentialRecord{digest: sha256.Sum256([]byte(token)), Scopes: []Scope{ScopeAdmin}})
	}
	for _, credential := range credentials {
		records = append(records, credentialRecord{digest: sha256.Sum256([]byte(credential.Token)), Scopes: slices.Clone(credential.Scopes), expiresAt: credential.ExpiresAt})
	}
	a.mu.Lock()
	a.credentials = records
	a.mu.Unlock()
	return nil
}

func (a *TokenAuthenticator) Valid(token string) bool {
	_, ok := a.Authenticate(token)
	return ok
}

func (a *TokenAuthenticator) Authenticate(token string) (string, bool) {
	return a.Authorize(token, "")
}

// Authorize validates token expiry and required scope and returns its stable,
// non-secret fingerprint. Admin credentials grant read and write operations.
func (a *TokenAuthenticator) Authorize(token string, required Scope) (string, bool) {
	if token == "" {
		return "", false
	}
	digest := sha256.Sum256([]byte(token))
	now := time.Now()
	a.mu.RLock()
	match := 0
	for _, credential := range a.credentials {
		constantMatch := subtle.ConstantTimeCompare(digest[:], credential.digest[:])
		valid := credential.expiresAt.IsZero() || now.Before(credential.expiresAt)
		scoped := required == ""
		for _, scope := range credential.Scopes {
			scoped = scoped || scope == ScopeAdmin || scope == required
		}
		match |= constantMatch & boolToInt(valid && scoped)
	}
	a.mu.RUnlock()
	if match != 1 {
		return "", false
	}
	return hex.EncodeToString(digest[:12]), true
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func AuthenticatedTokenID(ctx context.Context) string {
	id, _ := ctx.Value(tokenIDKey{}).(string)
	return id
}

// Middleware rejects invalid, expired, or under-scoped bearer credentials.
func (a *TokenAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
			writeUnauthorized(w)
			return
		}
		scope := RequiredScope(r.Method, r.URL.Path)
		id, ok := a.Authorize(header[len(prefix):], scope)
		if !ok {
			if a.AuthenticateTokenOnly(header[len(prefix):]) {
				http.Error(w, `{"error":"insufficient_scope"}`, http.StatusForbidden)
				return
			}
			writeUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenIDKey{}, id)))
	})
}

func (a *TokenAuthenticator) AuthenticateTokenOnly(token string) bool {
	_, ok := a.Authorize(token, "")
	return ok
}

func RequiredScope(method, _ string) Scope {
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
		return ScopeWrite
	}
	return ScopeRead
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="atlas"`)
	http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
}
