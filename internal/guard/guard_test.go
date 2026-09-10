package guard

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestGenerateTripcode(t *testing.T) {
	a := GenerateTripcode("secret", 8)
	b := GenerateTripcode("secret", 8)
	if a == "" || a != b {
		t.Fatalf("tripcode not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "◆ ") || utf8.RuneCountInString(a) != 2+8 {
		t.Fatalf("bad tripcode shape: %q", a)
	}
	if GenerateTripcode("secret", 8) == GenerateTripcode("other", 8) {
		t.Fatal("different secrets collide")
	}
	if GenerateTripcode("", 8) != "" || GenerateTripcode("s", 0) != "" {
		t.Fatal("empty secret/length must yield empty")
	}
	// Length clamps to hex output, never panics.
	if got := GenerateTripcode("s", 999); utf8.RuneCountInString(got) != 2+64 {
		t.Fatalf("oversize length not clamped: %q", got)
	}
}

func TestTripBadgeFromPubHex(t *testing.T) {
	if got := TripBadgeFromPubHex("zzzz"); got != "" {
		t.Fatalf("junk hex must yield empty, got %q", got)
	}
	if got := TripBadgeFromPubHex(""); got != "" {
		t.Fatalf("empty hex must yield empty, got %q", got)
	}
	pub := strings.Repeat("ab", 32)
	a, b := TripBadgeFromPubHex(pub), TripBadgeFromPubHex(pub)
	if a == "" || a != b || !strings.HasPrefix(a, "◆ ") {
		t.Fatalf("badge not deterministic: %q vs %q", a, b)
	}
}

func TestPenaltyAndBan(t *testing.T) {
	now := time.Now()
	var rec RateLimitRecord
	for i := 0; i < 4; i++ {
		rec = NextPenalty(rec, now)
		if IsBanned(rec, now) {
			t.Fatalf("banned after %d fails", i+1)
		}
	}
	rec = NextPenalty(rec, now)
	if !IsBanned(rec, now) {
		t.Fatal("5 fails must ban")
	}
	if IsBanned(rec, now.Add(6*time.Minute)) {
		t.Fatal("ban must expire after 5 minutes")
	}
}

func TestCheckConnectionRate(t *testing.T) {
	now := time.Now()
	banned := NextPenalty(NextPenalty(NextPenalty(NextPenalty(NextPenalty(RateLimitRecord{}, now), now), now), now), now)
	if ok, reason := CheckConnectionRate(now, banned, time.Time{}, time.Second); ok || reason != "banned" {
		t.Fatalf("banned must fail: ok=%v reason=%q", ok, reason)
	}
	if ok, reason := CheckConnectionRate(now, RateLimitRecord{}, now, time.Minute); ok || reason != "cooldown" {
		t.Fatalf("rapid reconnect must fail: ok=%v reason=%q", ok, reason)
	}
	if ok, _ := CheckConnectionRate(now, RateLimitRecord{}, now.Add(-time.Hour), time.Second); !ok {
		t.Fatal("stale connection must pass")
	}
}

func TestCooldownMap(t *testing.T) {
	c := NewCooldownMap()
	if !c.Allow("1.2.3.4", time.Minute) {
		t.Fatal("first hit must pass")
	}
	if c.Allow("1.2.3.4", time.Minute) {
		t.Fatal("immediate second hit must fail")
	}
	if !c.Allow("5.6.7.8", time.Minute) {
		t.Fatal("other IP must pass")
	}
}

func TestValidateMessageForSend(t *testing.T) {
	cfg := &Limits{MaxMessageLength: 5000, MaxMessageLine: 50, MessageCooldown: 200 * time.Millisecond}
	if err := ValidateMessageForSend("hello", time.Now().Add(-time.Hour), cfg, false); err != nil {
		t.Fatalf("good message rejected: %v", err)
	}
	if err := ValidateMessageForSend(strings.Repeat("x", cfg.MaxMessageLength+1), time.Now().Add(-time.Hour), cfg, false); err != ErrTooLong {
		t.Fatalf("oversize must be ErrTooLong, got %v", err)
	}
	manyLines := strings.Repeat("a\n", cfg.MaxMessageLine+1)
	if err := ValidateMessageForSend(manyLines, time.Now().Add(-time.Hour), cfg, false); err != ErrTooManyLines {
		t.Fatalf("too many lines must be ErrTooManyLines, got %v", err)
	}
	// Boundary: exactly MaxMessageLine lines pass.
	exact := strings.Repeat("a\n", cfg.MaxMessageLine-1) + "a"
	if err := ValidateMessageForSend(exact, time.Now().Add(-time.Hour), cfg, false); err != nil {
		t.Fatalf("exactly %d lines rejected: %v", cfg.MaxMessageLine, err)
	}
	if err := ValidateMessageForSend("hi", time.Now(), cfg, false); err != ErrTooFast {
		t.Fatalf("immediate resend must be ErrTooFast, got %v", err)
	}
	if err := ValidateMessageForSend(strings.Repeat("x", 1<<20), time.Now().Add(-time.Hour), cfg, true); err == ErrTooLong {
		t.Fatal("unlimited must skip length gate")
	}
	if err := ValidateMessageForSend("hi", time.Now(), nil, false); err != nil {
		t.Fatalf("nil cfg must not gate: %v", err)
	}
}

func TestCooldownMapEvictsStale(t *testing.T) {
	c := NewCooldownMap()
	// Fill past the cap with stale entries plus one fresh.
	for i := 0; i < 1005; i++ {
		c.last[ipForIndex(i)] = time.Now().Add(-time.Hour)
	}
	c.last["fresh"] = time.Now()
	if !c.Allow("newcomer", time.Minute) {
		t.Fatal("newcomer must pass")
	}
	c.mu.Lock()
	n := len(c.last)
	_, freshKept := c.last["fresh"]
	c.mu.Unlock()
	if !freshKept {
		t.Fatal("fresh entry must survive eviction")
	}
	if n > 1006 {
		t.Fatalf("map grew unbounded: %d", n)
	}
}

func ipForIndex(i int) string {
	return "10.9.0." + strconv.Itoa(i)
}
