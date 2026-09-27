// Package middleware holds the control-plane API server's net/http middleware
// (ADR-0018). authn resolves a static bearer token / scoped API key to an
// auth.Identity and gates every API request; unauthenticated requests get 401
// problem+json. The spec/docs endpoints stay public.
package middleware

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
)

// CredentialStore resolves an opaque bearer token / API key to an Identity. It
// returns fault.Unauthorized if the credential is unknown. The V1 implementation is
// an in-memory map built from config (NewStaticCredentials); OIDC is a V2 follow-on.
type CredentialStore interface {
	Lookup(ctx context.Context, token string) (auth.Identity, error)
}

type identityKey struct{}

// IdentityFrom returns the authenticated Identity placed in ctx by Authn.
func IdentityFrom(ctx context.Context) (auth.Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(auth.Identity)
	return id, ok
}

// withIdentity returns a request carrying id (used by Authn; exported variant is for tests).
func withIdentity(r *http.Request, id auth.Identity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
}

// isPublic reports whether the path is served without authentication (the OpenAPI
// spec, the schemas, and the docs UI). Everything under /apis is protected.
func isPublic(path string) bool {
	return path == "/openapi" || strings.HasPrefix(path, "/openapi.") ||
		path == "/docs" || strings.HasPrefix(path, "/docs/") ||
		strings.HasPrefix(path, "/schemas/")
}

// Authn returns middleware that authenticates each request: it reads the bearer token
// (Authorization: Bearer <t>) or the X-Api-Key header, resolves it via creds, and puts
// the Identity in the request context. Missing/unknown credentials → 401 problem+json
// (public spec/docs paths pass through).
func Authn(creds CredentialStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			token := bearerToken(r)
			if token == "" {
				fault.WriteProblem(w, fault.Unauthorizedf("controlplane.authn", "missing bearer token or API key"))
				return
			}
			id, err := creds.Lookup(r.Context(), token)
			if err != nil {
				fault.WriteProblem(w, fault.Unauthorizedf("controlplane.authn", "invalid credential"))
				return
			}
			next.ServeHTTP(w, withIdentity(r, id))
		})
	}
}

// bearerToken extracts the credential from the Authorization (Bearer) or X-Api-Key header.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
			return strings.TrimSpace(h[len(prefix):])
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// staticCredentials is an in-memory CredentialStore (config-injected for V1).
type staticCredentials struct {
	byToken map[string]auth.Identity
}

// NewStaticCredentials builds an in-memory CredentialStore from a token→Identity map.
func NewStaticCredentials(byToken map[string]auth.Identity) CredentialStore {
	cp := make(map[string]auth.Identity, len(byToken))
	for k, v := range byToken {
		cp[k] = v
	}
	return &staticCredentials{byToken: cp}
}

// Lookup resolves a token to its Identity with a constant-time compare against every
// configured token (so a miss does not leak timing about which token prefix matched).
func (s *staticCredentials) Lookup(_ context.Context, token string) (auth.Identity, error) {
	var (
		found auth.Identity
		ok    bool
	)
	tb := []byte(token)
	for k, id := range s.byToken {
		if subtle.ConstantTimeCompare([]byte(k), tb) == 1 {
			found, ok = id, true
		}
	}
	if !ok {
		return auth.Identity{}, fault.Unauthorizedf("controlplane.authn", "unknown credential")
	}
	return found, nil
}
