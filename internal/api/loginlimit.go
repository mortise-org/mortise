package api

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Login throttling defaults; override via env (MORTISE_LOGIN_*). In-memory is
// deliberate: the operator is single-replica by design, and a shared store
// would be a heavier dependency than the threat warrants.
const (
	defaultLoginRatePerMin     = 5
	defaultLoginBurst          = 10
	defaultLoginLockoutAfter   = 10
	defaultLoginLockoutMinutes = 15

	// loginLimiterMaxEntries bounds memory under a key-spray attack. When the
	// map is full, idle entries are pruned; if none are prunable the limiter
	// fails open for new keys (a full table must not lock out real users).
	loginLimiterMaxEntries = 10000
	loginLimiterIdleEvict  = 30 * time.Minute
)

type loginEntry struct {
	tokens      float64
	lastRefill  time.Time
	failures    int
	lockedUntil time.Time
	lastSeen    time.Time
}

// loginLimiter is a per-key token bucket with a sliding lockout on repeated
// failures. Keys combine the lowercased username with the direct peer IP
// (RemoteAddr, never X-Forwarded-For: XFF is attacker-controlled, and behind
// a single ingress the per-username component does the real work anyway).
type loginLimiter struct {
	mu  sync.Mutex
	now func() time.Time

	ratePerMin   float64
	burst        float64
	lockoutAfter int
	lockoutDur   time.Duration

	entries map[string]*loginEntry
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{
		now:          now,
		ratePerMin:   float64(envInt("MORTISE_LOGIN_RATE_PER_MIN", defaultLoginRatePerMin)),
		burst:        float64(envInt("MORTISE_LOGIN_BURST", defaultLoginBurst)),
		lockoutAfter: envInt("MORTISE_LOGIN_LOCKOUT_FAILURES", defaultLoginLockoutAfter),
		lockoutDur:   time.Duration(envInt("MORTISE_LOGIN_LOCKOUT_MINUTES", defaultLoginLockoutMinutes)) * time.Minute,
		entries:      make(map[string]*loginEntry),
	}
}

func loginRateKey(email string, r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return strings.ToLower(strings.TrimSpace(email)) + "|" + host
}

// allow reports whether a login attempt for key may proceed. When it may not,
// retryAfter is how long the caller should advertise via Retry-After.
func (l *loginLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	e := l.entries[key]
	if e == nil {
		if len(l.entries) >= loginLimiterMaxEntries {
			l.pruneLocked(now)
		}
		if len(l.entries) >= loginLimiterMaxEntries {
			return true, 0
		}
		e = &loginEntry{tokens: l.burst, lastRefill: now}
		l.entries[key] = e
	}
	e.lastSeen = now

	if now.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(now)
	}

	elapsed := now.Sub(e.lastRefill).Minutes()
	e.tokens = min(l.burst, e.tokens+elapsed*l.ratePerMin)
	e.lastRefill = now

	if e.tokens >= 1 {
		e.tokens--
		return true, 0
	}
	need := (1 - e.tokens) / l.ratePerMin
	return false, time.Duration(need * float64(time.Minute))
}

// recordFailure counts a failed authentication. Reaching the threshold locks
// the key out; further failures while locked out slide the window forward.
func (l *loginLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	e := l.entries[key]
	if e == nil {
		// Same cap discipline as allow(): a new key under a full table gets
		// neither throttling nor failure-tracking. Without this, failed logins
		// for distinct usernames grow the map without bound (allow() fails open
		// without inserting, so recordFailure is the only insert path) — the
		// unbounded-process-memory class #447 paid for.
		if len(l.entries) >= loginLimiterMaxEntries {
			l.pruneLocked(now)
		}
		if len(l.entries) >= loginLimiterMaxEntries {
			return
		}
		e = &loginEntry{tokens: l.burst, lastRefill: now}
		l.entries[key] = e
	}
	e.lastSeen = now
	e.failures++
	if e.failures >= l.lockoutAfter {
		e.lockedUntil = now.Add(l.lockoutDur)
	}
}

// recordSuccess clears all throttling state for the key — a successful login
// proves the caller is not guessing.
func (l *loginLimiter) recordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// pruneLocked drops entries idle past the eviction window. Callers hold l.mu.
func (l *loginLimiter) pruneLocked(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.lastSeen) > loginLimiterIdleEvict && now.After(e.lockedUntil) {
			delete(l.entries, k)
		}
	}
}
