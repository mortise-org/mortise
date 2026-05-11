package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/mortise-org/mortise/internal/auth"
	kclock "k8s.io/utils/clock"
)

const (
	sseTokenTTL    = 30 * time.Second
	sseTokenBytes  = 32
	sseTokenPrefix = "msse_"
	cleanupEvery   = 60 * time.Second
)

type sseTokenPrincipal struct {
	email       string
	passwordGen int64
}
type sseTokenEntry struct {
	principal sseTokenPrincipal
	expiresAt time.Time
}

type sseTokenStore struct {
	mu      sync.Mutex
	tokens  map[string]sseTokenEntry
	stopCh  chan struct{}
	stopped bool
	clock   kclock.Clock
}

func newSSETokenStore(clk kclock.Clock) *sseTokenStore {
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
		principal: sseTokenPrincipal{
			email:       p.Email,
			passwordGen: p.PasswordGen,
		},
		expiresAt: s.clock.Now().Add(sseTokenTTL),
	}
	s.mu.Unlock()

	return token, nil
}

// Redeem validates and consumes an SSE token (single-use).
func (s *sseTokenStore) Redeem(token string) (sseTokenPrincipal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.tokens[token]
	if !ok {
		return sseTokenPrincipal{}, false
	}
	delete(s.tokens, token)

	if s.clock.Now().After(entry.expiresAt) {
		return sseTokenPrincipal{}, false
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

// @Summary Issue short-lived SSE token
// @Description Exchanges a bearer-authenticated session for a short-lived, single-use opaque SSE token. Redeem revalidates the user against the auth provider before opening the stream.
// @Tags auth
// @Produce json
// @Security BearerAuth
// @Success 200 {object} sseTokenResponse
// @Failure 401 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Router /auth/sse-token [post]
//
// IssueSSEToken handles POST /api/auth/sse-token. It returns a short-lived,
// single-use opaque token that the client can pass as ?token= on SSE endpoints
// instead of the full JWT.
func (s *Server) IssueSSEToken(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return
	}

	revalidated, err := s.revalidatePrincipal(r.Context(), p.Email, p.PasswordGen)
	if err != nil {
		switch {
		case errors.Is(err, errCurrentPrincipalUnavailable):
			writeJSON(w, http.StatusUnauthorized, errorResponse{"SSE token unavailable for this auth provider"})
			return
		case errors.Is(err, auth.ErrPasswordChangeInvalidated):
			writeJSON(w, http.StatusUnauthorized, errorResponse{"session invalidated by password change"})
			return
		case errors.Is(err, auth.ErrUserNotFound):
			writeJSON(w, http.StatusUnauthorized, errorResponse{"user no longer exists"})
			return
		default:
			writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
			return
		}
	}

	token, err := s.sseTokens.Issue(revalidated)
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
