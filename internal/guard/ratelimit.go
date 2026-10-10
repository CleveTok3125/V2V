package guard

import (
	"sync"
	"time"
)

type CooldownMap struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func NewCooldownMap() *CooldownMap { return &CooldownMap{last: make(map[string]time.Time)} }

// Reset forgets every recorded call. Only tests need this: the cooldown
// maps are package-level, so without it one test's held cooldown silently
// refuses the next test's request.
func (c *CooldownMap) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = make(map[string]time.Time)
}

func (c *CooldownMap) Allow(ip string, d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if t, ok := c.last[ip]; ok && time.Since(t) < d {
		return false
	}
	c.last[ip] = now
	if len(c.last) > 1000 {
		for k, v := range c.last {
			if time.Since(v) > 10*time.Minute {
				delete(c.last, k)
			}
		}
	}
	return true
}

// RetryAfter reports how much of a refusal is left for one address, so a
// handler can tell the caller how long to wait instead of leaving it to
// guess. Zero means nothing is held. Callers read it after Allow refused:
// the recorded time is what Allow stores.
func (c *CooldownMap) RetryAfter(ip string, d time.Duration) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.last[ip]
	if !ok {
		return 0
	}
	if left := d - time.Since(t); left > 0 {
		return left
	}
	return 0
}
