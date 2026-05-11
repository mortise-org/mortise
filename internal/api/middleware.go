package api

import (
	"context"
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
// Applied only to protected routes; public auth endpoints bypass it, while
// protected auth subroutes opt in explicitly where mounted.
func (s *Server) jwtAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if PrincipalFromContext(r.Context()) != nil {
			next.ServeHTTP(w, r)
			return
		}

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

		principal, err := s.auth.Principal(r.Context(), auth.SessionToken(token))
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
				principal, err := s.auth.Principal(r.Context(), auth.SessionToken(token))
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

// sseAuthMiddleware handles authentication for SSE endpoints. It accepts only
// a short-lived, single-use SSE token in the ?token= query parameter.
func (s *Server) sseAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If Authorization header is already set, let jwtAuthMiddleware handle it.
		if r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
			return
		}

		tok := r.URL.Query().Get("token")
		if tok == "" {
			next.ServeHTTP(w, r)
			return
		}

		if strings.HasPrefix(tok, sseTokenPrefix) {
			principal, ok := s.sseTokens.Redeem(tok)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid or expired SSE token"})
				return
			}
			ctx := context.WithValue(r.Context(), principalKey, &principal)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		writeJSON(w, http.StatusUnauthorized, errorResponse{"invalid or expired SSE token"})
	})
}
