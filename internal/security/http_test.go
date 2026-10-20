package security

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func TestTokenAuth(t *testing.T) {
	good := strings.Repeat("a", 40)
	auth := NewTokenAuthenticator([]string{good, strings.Repeat("b", 40)})
	wantID, valid := auth.Authenticate(good)
	if !valid || len(wantID) != 24 {
		t.Fatalf("Authenticate() = %q, %v; want a 96-bit fingerprint", wantID, valid)
	}
	h := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "bearer ")
		expected, ok := auth.Authenticate(bearer)
		if !ok || AuthenticatedTokenID(r.Context()) != expected {
			t.Errorf("authenticated token fingerprint = %q, want %q", AuthenticatedTokenID(r.Context()), expected)
		}
		w.WriteHeader(http.StatusOK)
	}))

	cases := map[string]struct {
		header string
		want   int
	}{
		"valid":                   {"Bearer " + good, http.StatusOK},
		"case-insensitive scheme": {"bearer " + good, http.StatusOK},
		"second token":            {"Bearer " + strings.Repeat("b", 40), http.StatusOK},
		"missing":                 {"", http.StatusUnauthorized},
		"wrong token":             {"Bearer nope", http.StatusUnauthorized},
		"wrong scheme":            {"Basic " + good, http.StatusUnauthorized},
		"empty bearer":            {"Bearer ", http.StatusUnauthorized},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestTokenAuthenticatorCanRotateCredentials(t *testing.T) {
	oldToken, newToken := strings.Repeat("o", 40), strings.Repeat("n", 40)
	auth := NewTokenAuthenticator([]string{oldToken})
	if !auth.Valid(oldToken) {
		t.Fatal("initial token not accepted")
	}
	auth.Replace([]string{newToken})
	if auth.Valid(oldToken) || !auth.Valid(newToken) {
		t.Fatal("token replacement did not apply atomically")
	}
}

func TestScopedCredentialsEnforceScopeAndExpiry(t *testing.T) {
	reader := strings.Repeat("r", 40)
	writer := strings.Repeat("w", 40)
	auth, err := NewCredentialAuthenticator([]Credential{
		{Token: reader, Scopes: []Scope{ScopeRead}, ExpiresAt: time.Now().Add(time.Hour)},
		{Token: writer, Scopes: []Scope{ScopeWrite}, ExpiresAt: time.Now().Add(30 * time.Millisecond)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.Authorize(reader, ScopeRead); !ok {
		t.Fatal("read credential was rejected for read")
	}
	if _, ok := auth.Authorize(reader, ScopeWrite); ok {
		t.Fatal("read credential was accepted for write")
	}
	if _, ok := auth.Authorize(writer, ScopeWrite); !ok {
		t.Fatal("write credential was rejected before expiry")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := auth.Authorize(writer, ScopeWrite); ok {
		t.Fatal("expired credential was accepted")
	}
	h := auth.Middleware(ok)
	req := httptest.NewRequest(http.MethodPost, "/v1/services", nil)
	req.Header.Set("Authorization", "Bearer "+reader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("under-scoped HTTP request status = %d, want 403", rec.Code)
	}
}

func TestRateLimiter(t *testing.T) {
	h := NewRateLimiter(1, 2).Middleware(ok)
	codes := make([]int, 0, 3)
	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want [200 200 429]", codes)
	}

	// A different client has its own bucket.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second client got %d", rec.Code)
	}
}

func TestRateLimiterUpdateAffectsExistingClientBuckets(t *testing.T) {
	limiter := NewRateLimiter(0.01, 1)
	if !limiter.AllowClient("192.0.2.1:1234", "") || limiter.AllowClient("192.0.2.1:1234", "") {
		t.Fatal("initial bucket did not apply")
	}
	limiter.Update(100, 3)
	time.Sleep(50 * time.Millisecond) // allow the existing bucket to refill at the new rate
	for range 3 {
		if !limiter.AllowClient("192.0.2.1:1234", "") {
			t.Fatal("updated burst did not apply to existing client")
		}
	}
}

func TestRateLimiterTrustedProxyUsesForwardedClient(t *testing.T) {
	_, subnet, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRateLimiter(1, 1, subnet).Middleware(ok)

	request := func(client string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.1.2.3:443"
		req.Header.Set("X-Forwarded-For", client+", 10.2.3.4")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := request("198.51.100.10"); code != http.StatusOK {
		t.Fatalf("first client request = %d, want 200", code)
	}
	if code := request("198.51.100.11"); code != http.StatusOK {
		t.Fatalf("second client request = %d, want 200", code)
	}
	if code := request("198.51.100.10"); code != http.StatusTooManyRequests {
		t.Fatalf("repeated client request = %d, want 429", code)
	}
}

func TestRateLimiterIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	_, subnet, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRateLimiter(1, 1, subnet).Middleware(ok)
	for i, client := range []string{"198.51.100.10", "198.51.100.11"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "203.0.113.5:1234"
		req.Header.Set("X-Forwarded-For", client)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		want := http.StatusOK
		if i == 1 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("request %d with untrusted X-Forwarded-For = %d, want %d", i, rec.Code, want)
		}
	}
}

func TestSecureHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SecureHeaders(ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, k := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Cache-Control"} {
		if rec.Header().Get(k) == "" {
			t.Errorf("missing header %s", k)
		}
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must not be sent over plain HTTP")
	}
}
