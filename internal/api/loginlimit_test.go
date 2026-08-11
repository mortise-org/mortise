package api

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func newTestLimiter(start time.Time) (*loginLimiter, *time.Time) {
	now := start
	l := newLoginLimiter(func() time.Time { return now })
	return l, &now
}

func TestLoginLimiterBurstThenThrottle(t *testing.T) {
	l, _ := newTestLimiter(time.Unix(1_700_000_000, 0))

	for i := 0; i < defaultLoginBurst; i++ {
		if ok, _ := l.allow("u|ip"); !ok {
			t.Fatalf("attempt %d within burst should be allowed", i+1)
		}
	}
	ok, retryAfter := l.allow("u|ip")
	if ok {
		t.Fatal("attempt beyond burst should be throttled")
	}
	if retryAfter <= 0 || retryAfter > time.Minute {
		t.Fatalf("expected sub-minute positive Retry-After, got %v", retryAfter)
	}
}

func TestLoginLimiterTokensDecayBack(t *testing.T) {
	l, now := newTestLimiter(time.Unix(1_700_000_000, 0))

	for i := 0; i < defaultLoginBurst; i++ {
		l.allow("u|ip")
	}
	if ok, _ := l.allow("u|ip"); ok {
		t.Fatal("bucket should be empty")
	}

	// One minute refills ratePerMin tokens.
	*now = now.Add(time.Minute)
	for i := 0; i < defaultLoginRatePerMin; i++ {
		if ok, _ := l.allow("u|ip"); !ok {
			t.Fatalf("refilled attempt %d should be allowed", i+1)
		}
	}
	if ok, _ := l.allow("u|ip"); ok {
		t.Fatal("refill should be capped at ratePerMin after one minute")
	}
}

func TestLoginLimiterLockoutSlidesAndExpires(t *testing.T) {
	l, now := newTestLimiter(time.Unix(1_700_000_000, 0))

	for i := 0; i < defaultLoginLockoutAfter; i++ {
		l.recordFailure("u|ip")
	}
	ok, retryAfter := l.allow("u|ip")
	if ok {
		t.Fatal("expected lockout after threshold failures")
	}
	if retryAfter != time.Duration(defaultLoginLockoutMinutes)*time.Minute {
		t.Fatalf("expected full lockout window, got %v", retryAfter)
	}

	// A failure mid-lockout slides the window forward.
	*now = now.Add(10 * time.Minute)
	l.recordFailure("u|ip")
	if _, retryAfter = l.allow("u|ip"); retryAfter != time.Duration(defaultLoginLockoutMinutes)*time.Minute {
		t.Fatalf("expected slid lockout window, got %v", retryAfter)
	}

	// After the window passes with no failures, attempts flow again.
	*now = now.Add(time.Duration(defaultLoginLockoutMinutes)*time.Minute + time.Second)
	if ok, _ := l.allow("u|ip"); !ok {
		t.Fatal("expected lockout to expire")
	}
}

func TestLoginLimiterSuccessResets(t *testing.T) {
	l, _ := newTestLimiter(time.Unix(1_700_000_000, 0))

	for i := 0; i < defaultLoginLockoutAfter-1; i++ {
		l.recordFailure("u|ip")
	}
	l.recordSuccess("u|ip")

	// The failure streak is gone: threshold-1 more failures do not lock out,
	// and the bucket is back at full burst.
	for i := 0; i < defaultLoginLockoutAfter-1; i++ {
		l.recordFailure("u|ip")
	}
	if ok, _ := l.allow("u|ip"); !ok {
		t.Fatal("success should have reset the failure streak")
	}
}

func TestLoginLimiterKeysAreIndependent(t *testing.T) {
	l, _ := newTestLimiter(time.Unix(1_700_000_000, 0))

	for i := 0; i < defaultLoginBurst; i++ {
		l.allow("alice|1.2.3.4")
	}
	if ok, _ := l.allow("alice|1.2.3.4"); ok {
		t.Fatal("alice's bucket should be empty")
	}
	if ok, _ := l.allow("bob|1.2.3.4"); !ok {
		t.Fatal("bob must not be throttled by alice's attempts")
	}
	if ok, _ := l.allow("alice|5.6.7.8"); !ok {
		t.Fatal("alice from another IP must have her own bucket")
	}
}

func TestLoginLimiterFailsOpenWhenFull(t *testing.T) {
	l, _ := newTestLimiter(time.Unix(1_700_000_000, 0))
	for i := 0; i < loginLimiterMaxEntries; i++ {
		l.entries[string(rune(i))+"-filler"] = &loginEntry{lastSeen: l.now()}
	}
	// All entries are fresh (nothing prunable): a NEW key must not be denied
	// service by the table being full.
	if ok, _ := l.allow("fresh|key"); !ok {
		t.Fatal("limiter must fail open for new keys when the table is full")
	}
}

func TestLoginLimiterRecordFailureRespectsCap(t *testing.T) {
	l, _ := newTestLimiter(time.Unix(1_700_000_000, 0))
	// Fill with fresh (unprunable) entries so the cap is genuinely hit.
	for i := 0; i < loginLimiterMaxEntries; i++ {
		l.entries[string(rune(i))+"-filler"] = &loginEntry{lastSeen: l.now()}
	}
	// recordFailure is the only insert path when allow() fails open, so a
	// spray of distinct failed-login keys must not grow the map past the cap.
	for i := 0; i < 100; i++ {
		l.recordFailure("spray-" + strconv.Itoa(i))
	}
	if len(l.entries) > loginLimiterMaxEntries {
		t.Fatalf("recordFailure grew the map past the cap: %d > %d", len(l.entries), loginLimiterMaxEntries)
	}
}

func TestLoginRateKeyShape(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "10.1.2.3:5555"
	if got := loginRateKey("  Alice@Example.COM ", r); got != "alice@example.com|10.1.2.3" {
		t.Fatalf("unexpected key %q", got)
	}
}
