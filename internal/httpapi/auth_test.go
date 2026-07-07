package httpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "https://seven-swords.net"
	testAudience = "kowloon"
)

// newTestAuth wires the middleware around a static ES256 public key,
// bypassing JWKS fetching so the tests exercise the verification logic
// (alg pinning, iss/aud/exp checks, header parsing) directly.
func newTestAuth(t *testing.T) (bearerAuth, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	a := bearerAuth{
		keyfunc:  func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		issuer:   testIssuer,
		audience: testAudience,
	}
	return a, key
}

func sign(t *testing.T, key *ecdsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss": testIssuer,
		"aud": testAudience,
		"exp": time.Now().Add(time.Hour).Unix(),
	}
}

func serve(a bearerAuth, req *http.Request) *httptest.ResponseRecorder {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	a.middleware(next).ServeHTTP(rec, req)
	return rec
}

func TestBearerAuth_ValidToken(t *testing.T) {
	a, key := newTestAuth(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/search", nil)
	req.Header.Set("Authorization", "Bearer "+sign(t, key, validClaims()))
	if rec := serve(a, req); rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
}

func TestBearerAuth_HealthzOpen(t *testing.T) {
	a, _ := newTestAuth(t)
	// No Authorization header, yet /healthz must pass through.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	if rec := serve(a, req); rec.Code != http.StatusOK {
		t.Fatalf("healthz code=%d, want 200 (open)", rec.Code)
	}
}

func TestBearerAuth_Rejects(t *testing.T) {
	a, key := newTestAuth(t)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	cases := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"not bearer", "Basic abc"},
		{"garbage token", "Bearer not-a-jwt"},
		{"wrong issuer", "Bearer " + sign(t, key, jwt.MapClaims{"iss": "https://evil.example", "aud": testAudience, "exp": time.Now().Add(time.Hour).Unix()})},
		{"wrong audience", "Bearer " + sign(t, key, jwt.MapClaims{"iss": testIssuer, "aud": "someone-else", "exp": time.Now().Add(time.Hour).Unix()})},
		{"expired", "Bearer " + sign(t, key, jwt.MapClaims{"iss": testIssuer, "aud": testAudience, "exp": time.Now().Add(-time.Hour).Unix()})},
		{"no expiry", "Bearer " + sign(t, key, jwt.MapClaims{"iss": testIssuer, "aud": testAudience})},
		{"signed by other key", "Bearer " + sign(t, other, validClaims())},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/search", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			if rec := serve(a, req); rec.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d, want 401", rec.Code)
			}
		})
	}
}

func TestBearerAuth_HMACAlgRejected(t *testing.T) {
	// A token signed with HS256 must be rejected by the ES256 method pin
	// even though the header claims a valid-looking token — otherwise an
	// attacker could sign with the public key as an HMAC secret.
	a, _ := newTestAuth(t)
	hs, err := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims()).SignedString([]byte("secret"))
	if err != nil {
		t.Fatalf("sign hs256: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/search", nil)
	req.Header.Set("Authorization", "Bearer "+hs)
	if rec := serve(a, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, want 401 (HS256 must be rejected)", rec.Code)
	}
}

func TestNewBearerAuth_DisabledWhenNoIssuer(t *testing.T) {
	mw, err := NewBearerAuth(OIDCConfig{})
	if err != nil {
		t.Fatalf("NewBearerAuth: %v", err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodPost, "/v1/search", nil) // no token
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200 (auth disabled)", rec.Code)
	}
}
