package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/MC-Meesh/mortise/internal/auth"
)

type contextKey string

const principalKey contextKey = "principal"

// PrincipalFromContext extracts the authenticated principal from the request context.
func PrincipalFromContext(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(principalKey).(*auth.Principal)
	return p
}

// jwtAuthMiddleware validates a Bearer JWT via the server's JWTHelper.
// Applied only to protected /api routes — not to UI paths or /api/auth/*.
func (s *Server) jwtAuthMiddleware(next http.Handler) http.Handler {
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

		principal, err := s.jwt.ValidateToken(r.Context(), token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid token"})
			return
		}

		ctx := context.WithValue(r.Context(), principalKey, &principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
