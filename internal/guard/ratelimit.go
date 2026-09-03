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
