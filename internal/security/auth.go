package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// TokenAuthenticator validates static bearer tokens.
//
// Tokens are kept only as SHA-256 digests and compared in constant time
// against every configured digest, so neither timing nor early exit reveal
// which token (if any) matched. Multiple tokens allow rotation: add the new
// token, migrate clients, remove the old one.
type TokenAuthenticator struct {
	digests [][sha256.Size]byte
}

// NewTokenAuthenticator hashes tokens; the plaintext is not retained.
func NewTokenAuthenticator(tokens []string) *TokenAuthenticator {
	a := &TokenAuthenticator{}
	for _, t := range tokens {
		a.digests = append(a.digests, sha256.Sum256([]byte(t)))
	}
	return a
}

// Valid reports whether token matches a configured token.
func (a *TokenAuthenticator) Valid(token string) bool {
	if token == "" {
		return false
	}
	d := sha256.Sum256([]byte(token))
	match := 0
	for i := range a.digests {
		match |= subtle.ConstantTimeCompare(d[:], a.digests[i][:])
	}
	return match == 1
}

// Middleware rejects requests without a valid "Authorization: Bearer" header.
func (a *TokenAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) || !a.Valid(h[len(prefix):]) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="atlas"`)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
