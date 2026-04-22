package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mortise-org/mortise/internal/auth"
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

// optionalJWTMiddleware validates a Bearer JWT if present and non-mrt_. If
// the token is a deploy token (mrt_ prefix) or absent, the request proceeds
// without a principal — the handler is responsible for checking deploy token
// auth itself.
func (s *Server) optionalJWTMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header != "" && strings.HasPrefix(header, "Bearer ") {
			token := strings.TrimPrefix(header, "Bearer ")
			if token != "" && !strings.HasPrefix(token, "mrt_") {
				principal, err := s.jwt.ValidateToken(r.Context(), token)
				if err != nil {
					writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid token"})
					return
				}
				ctx := context.WithValue(r.Context(), principalKey, &principal)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// maxBytesMiddleware limits the size of incoming request bodies. Requests
// that exceed the limit receive 413 Request Entity Too Large.
func maxBytesMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// sseTokenQueryParamMiddleware allows the SSE log-stream endpoint to accept
// a `?token=<jwt>` query param as an alternative to the Authorization header.
// This is the standard workaround for EventSource, which cannot set custom
// headers. If the Authorization header is already present, this is a no-op.
//
// The token value itself is never logged; only its presence is noted at debug
// level.
func sseTokenQueryParamMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			if tok := r.URL.Query().Get("token"); tok != "" {
				r.Header.Set("Authorization", "Bearer "+tok)
				slog.Debug("sse: using ?token= query param for authorization", "path", r.URL.Path)
			}
		}
		next.ServeHTTP(w, r)
	})
}
