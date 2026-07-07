package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// OIDCConfig configures bearer-token verification against an OIDC
// provider's JWKS — Asteroid (https://seven-swords.net) in this
// deployment. A zero Issuer disables auth entirely: NewBearerAuth then
// returns a pass-through middleware, so the loopback / dev deployment
// and the test suite keep working until the operator wires the env.
type OIDCConfig struct {
	// Issuer is the expected "iss" claim and, unless JWKSURL is set, the
	// base for the JWKS URL (<Issuer>/jwks.json). Empty disables auth.
	Issuer string

	// JWKSURL overrides the default <Issuer>/jwks.json — useful when the
	// key set is served from a path the issuer URL does not imply.
	JWKSURL string

	// Audience is the expected "aud" claim: the value the client was
	// registered with on the provider side. Empty skips the aud check.
	Audience string
}

// Middleware wraps an http.Handler with a cross-cutting concern.
type Middleware func(http.Handler) http.Handler

// NewBearerAuth builds a middleware that requires a valid ES256 bearer
// token on every route except /healthz. Asteroid signs access tokens
// with ES256 and rotates its signing keys, so verification goes through
// a JWKS keyfunc that refreshes in the background.
//
// When cfg.Issuer is empty the returned middleware is a no-op and the
// server stays unauthenticated. Otherwise the JWKS is fetched once here
// so a misconfigured URL fails fast at startup rather than on the first
// request.
func NewBearerAuth(cfg OIDCConfig) (Middleware, error) {
	if cfg.Issuer == "" {
		return func(next http.Handler) http.Handler { return next }, nil
	}

	jwksURL := cfg.JWKSURL
	if jwksURL == "" {
		jwksURL = strings.TrimRight(cfg.Issuer, "/") + "/jwks.json"
	}
	k, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("httpapi: init JWKS from %s: %w", jwksURL, err)
	}

	return bearerAuth{
		keyfunc:  k.Keyfunc,
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
	}.middleware, nil
}

type bearerAuth struct {
	keyfunc  jwt.Keyfunc
	issuer   string
	audience string
}

func (a bearerAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /healthz stays open so the deploy's health probe needs no
		// token; every other route is deny-by-default.
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		raw := bearerToken(r)
		if raw == "" {
			unauthorized(w, "missing bearer token")
			return
		}

		opts := []jwt.ParserOption{
			// Pin ES256: without this a token could name "none" or an
			// HMAC alg and sidestep the public-key check.
			jwt.WithValidMethods([]string{"ES256"}),
			jwt.WithIssuer(a.issuer),
			jwt.WithExpirationRequired(),
		}
		if a.audience != "" {
			opts = append(opts, jwt.WithAudience(a.audience))
		}

		if tok, err := jwt.Parse(raw, a.keyfunc, opts...); err != nil || !tok.Valid {
			unauthorized(w, "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header. Returns "" when the header is absent or not a bearer scheme.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func unauthorized(w http.ResponseWriter, reason string) {
	writeError(w, http.StatusUnauthorized, errors.New(reason))
}
