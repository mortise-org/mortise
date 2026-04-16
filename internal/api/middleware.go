package api

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const principalKey contextKey = "principal"

// Principal represents an authenticated user.
type Principal struct {
	Subject string
}

// PrincipalFromContext extracts the authenticated principal from the request context.
func PrincipalFromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}

// JWTAuth is middleware that validates a Bearer JWT from the Authorization header.
// Currently stubbed: any non-empty Bearer token is accepted with subject "stub-user".
// The real implementation will be provided by the auth package.
func JWTAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, errorResponse{"missing or invalid Authorization header"})
			return
		}

		token := strings.TrimPrefix(header, "Bearer ")
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, errorResponse{"empty bearer token"})
			return
		}

		// Stub: accept any non-empty token. Real validation will use HS256 JWT verification.
		principal := &Principal{Subject: "stub-user"}
		ctx := context.WithValue(r.Context(), principalKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
