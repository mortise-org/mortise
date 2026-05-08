package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mortise-org/mortise/internal/auth"
	"k8s.io/utils/clock"
)

const (
	sseTokenTTL    = 30 * time.Second
	sseTokenBytes  = 32
	sseTokenPrefix = "msse_"
	cleanupEvery   = 60 * time.Second
)

type sseTokenEntry struct {
	principal auth.Principal
	expiresAt time.Time
}

type sseTokenStore struct {
	mu      sync.Mutex
	tokens  map[string]sseTokenEntry
	stopCh  chan struct{}
	stopped bool
	clock   clock.Clock
}

func newSSETokenStore(clk clock.Clock) *sseTokenStore {
	s := &sseTokenStore{
		tokens: make(map[string]sseTokenEntry),
		stopCh: make(chan struct{}),
		clock:  clk,
	}
	go s.cleanup()
	return s
}

func (s *sseTokenStore) Issue(p auth.Principal) (string, error) {
	raw := make([]byte, sseTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := sseTokenPrefix + hex.EncodeToString(raw)

	s.mu.Lock()
	s.tokens[token] = sseTokenEntry{
		principal: p,
		expiresAt: s.clock.Now().Add(sseTokenTTL),
	}
	s.mu.Unlock()

	return token, nil
}

// Redeem validates and consumes an SSE token (single-use).
func (s *sseTokenStore) Redeem(token string) (auth.Principal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.tokens[token]
	if !ok {
		return auth.Principal{}, false
	}
	delete(s.tokens, token)

	if s.clock.Now().After(entry.expiresAt) {
		return auth.Principal{}, false
	}
	return entry.principal, true
}

func (s *sseTokenStore) cleanup() {
	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			now := s.clock.Now()
			for k, v := range s.tokens {
				if now.After(v.expiresAt) {
					delete(s.tokens, k)
				}
			}
			s.mu.Unlock()
		case <-s.stopCh:
			return
		}
	}
}

func (s *sseTokenStore) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		close(s.stopCh)
		s.stopped = true
	}
}

// IssueSSEToken handles POST /api/auth/sse-token. It returns a short-lived,
// single-use opaque token that the client can pass as ?token= on SSE endpoints
// instead of the full JWT.
func (s *Server) IssueSSEToken(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return
	}

	token, err := s.sseTokens.Issue(*p)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to generate SSE token"})
		return
	}

	writeJSON(w, http.StatusOK, sseTokenResponse{
		Token:     token,
		ExpiresIn: int(sseTokenTTL.Seconds()),
	})
}

type sseTokenResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expiresIn"`
}

// RefreshToken handles POST /api/auth/refresh. It returns a new JWT if the
// current token is valid (the caller is already authenticated via middleware).
// Re-validates user state from the auth provider before issuing a new token
// to ensure the user has not been revoked or disabled since initial auth.
func (s *Server) RefreshToken(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return
	}

	// Re-validate user state before issuing a fresh token. The middleware
	// already validated the JWT, but the user may have been revoked or had
	// their password changed between the middleware check and this handler.
	header := r.Header.Get("Authorization")
	if header != "" && strings.HasPrefix(header, "Bearer ") {
		bearerToken := strings.TrimPrefix(header, "Bearer ")
		freshPrincipal, err := s.auth.Principal(r.Context(), auth.SessionToken(bearerToken))
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorResponse{"user session is no longer valid"})
			return
		}
		p = &freshPrincipal
	}

	token, err := s.jwt.GenerateToken(r.Context(), *p)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to generate token"})
		return
	}

	writeJSON(w, http.StatusOK, authResponse{Token: token, User: *p})
}

