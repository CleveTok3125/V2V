package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"

	"github.com/CleveTok3125/V2V/internal/trustedproxy"
)

// loadBlocklist parses the operator blocklist (one IP/CIDR per line,
// "#" comments, same rules as trust files). A missing file only warns
// and disables matching; malformed or world-writable files fail the
// boot, like every other trust input.
func loadBlocklist(path string) ([]*net.IPNet, string, error) {
	set, err := trustedproxy.LoadTrustFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Sprintf("⚠️ Blocklist: %s missing; IP blocking disabled", path), nil
		}
		return nil, "", err
	}
	return set.Nets, "", nil
}

// blocklisted reports whether ip matches the loaded blocklist.
// Unparseable input never matches.
func blocklisted(nets []*net.IPNet, ip string) bool {
	return trustedproxy.ContainsIP(nets, ip)
}

// isIPv4 reports whether ip parses as IPv4 (v4-mapped IPv6 counts:
// the peer really speaks v4).
func isIPv4(ip string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	return parsed != nil && parsed.To4() != nil
}

// clientCount returns the live registered session count.
func (s *ChatServer) clientCount() int {
	s.Hub.ClientsMu.RLock()
	defer s.Hub.ClientsMu.RUnlock()
	return len(s.Hub.Clients)
}

// overCap reports whether registered sessions plus in-flight
// handshakes reached MAX_TOTAL_CONNECTIONS. A nil abuse config (unit
// tests that never boot) disables the cap.
func (s *ChatServer) overCap() bool {
	a := Cfg.Abuse.Load()
	if a == nil || a.MaxTotalConnections <= 0 {
		return false
	}
	return s.clientCount()+int(atomic.LoadInt64(&s.Inflight)) >= a.MaxTotalConnections
}

// releaseInflight drops one handshake slot.
func (s *ChatServer) releaseInflight() {
	atomic.AddInt64(&s.Inflight, -1)
}
