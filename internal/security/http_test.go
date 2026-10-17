package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func TestTokenAuth(t *testing.T) {
	good := strings.Repeat("a", 40)
	h := NewTokenAuthenticator([]string{good, strings.Repeat("b", 40)}).Middleware(ok)

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
