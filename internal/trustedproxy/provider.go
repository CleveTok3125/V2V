// Package trustedproxy resolves the real client IP behind an
// explicit, operator-configured chain of reverse proxies. Each proxy
// flavor is a Provider module (none, cloudflare, direct): adding one
// means a new file implementing Provider plus one registration line.
// No proxy header is ever trusted from an address outside the
// configured trust sets, which the operator loads from per-module
// files (see list.go). There are no embedded IP defaults.
package trustedproxy

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
)

// Provider is one trusted-proxy module. Trust sets are injected at
// construction from the operator's files; providers never fetch or
// embed network data on their own.
type Provider interface {
	// Name is the chain token and the "<name>.txt" file stem.
	Name() string
	// HeaderTrust reports whether this provider asserts client IPs
	// via request headers. Direct-style providers (direct, none)
	// always resolve to RemoteAddr and never read headers.
	HeaderTrust() bool
	// NeedsFile reports whether a missing "<name>.txt" is fatal.
	// Header providers require their file; direct/none degrade to
	// an empty set.
	NeedsFile() bool
	// Trusted reports whether remoteIP belongs to this provider's
	// trust set. none always returns true.
	Trusted(remoteIP string) bool
	// ClientIP extracts the real client IP from the request. Called
	// only after Trusted and only when HeaderTrust is true;
	// ok=false means the header is missing or invalid (reject).
	ClientIP(r *http.Request) (ip string, ok bool)
}

var registry = map[string]func([]*net.IPNet) Provider{}

// Register adds a provider module. Called from init in each
// provider file; later providers of the same name win (tests).
func Register(name string, build func([]*net.IPNet) Provider) {
	registry[name] = build
}

// Known lists registered provider names, sorted, for error messages.
func Known() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Lookup returns a provider constructor by name for tooling (boot
// capability checks). Prefer ParseChain/NewChain for real builds.
func Lookup(name string) (func([]*net.IPNet) Provider, bool) {
	build, ok := registry[name]
	return build, ok
}

// ParseChain parses PROXY_PROVIDER ("cloudflare,direct"): comma
// separated, case-insensitive, deduped. Empty input — or the shipped
// sentinel "false" — is an error: the operator must state the chain
// explicitly, there is no implicit default (fail-closed at boot).
func ParseChain(raw string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false":
		return nil, fmt.Errorf("PROXY_PROVIDER is required: set it explicitly (known: %s)", strings.Join(Known(), ", "))
	}
	var names []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" || seen[name] {
			continue
		}
		if _, ok := registry[name]; !ok {
			return nil, fmt.Errorf("unknown proxy provider %q (known: %s)", part, strings.Join(Known(), ", "))
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("PROXY_PROVIDER is required (known: %s)", strings.Join(Known(), ", "))
	}
	return names, nil
}

// Chain resolves client IPs in fixed precedence: header-asserting
// providers first (in listed order), then direct, then none. Order in
// the config value does not matter; membership does.
type Chain struct {
	order []Provider
	names []string
}

// NewChain builds the precedence order from parsed names and their
// trust sets. A header-asserting provider with an empty trust set is
// rejected: selecting cloudflare without loading its ranges would
// silently reject (or wrongly degrade) every connection.
func NewChain(names []string, sets map[string][]*net.IPNet) (*Chain, error) {
	in := map[string]bool{}
	for _, name := range names {
		in[name] = true
	}
	var order []Provider
	for _, name := range names {
		if registry[name](nil).HeaderTrust() {
			if len(sets[name]) == 0 {
				return nil, fmt.Errorf("proxy provider %q has an empty trust set: load %s.txt or drop it from PROXY_PROVIDER", name, name)
			}
			order = append(order, registry[name](sets[name]))
		}
	}
	for _, name := range []string{"direct", "none"} {
		if in[name] {
			order = append(order, registry[name](sets[name]))
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("PROXY_PROVIDER selected nothing usable")
	}
	return &Chain{order: order, names: names}, nil
}

// Names returns the configured chain tokens in listed order.
func (c *Chain) Names() []string { return c.names }

// Outcome is one resolution decision, carrying everything the access
// log needs. Either ClientIP is set or Reject is true, never both.
type Outcome struct {
	ClientIP string
	RemoteIP string
	Provider string
	// Trusted is true only when the IP came from a provider header
	// (gates X-Forwarded-Proto and similar trust decisions).
	Trusted bool
	Reject  bool
	Reason  string
}

// Resolve maps one request to the real client IP. Header providers
// are consulted first; direct/none degrade to RemoteAddr; with no
// claimant the request is rejected (the chain has no none/direct
// fallback by operator choice).
func (c *Chain) Resolve(r *http.Request) Outcome {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	for _, p := range c.order {
		if !p.Trusted(remoteIP) {
			continue
		}
		if !p.HeaderTrust() {
			return Outcome{ClientIP: remoteIP, RemoteIP: remoteIP, Provider: p.Name()}
		}
		if ip, ok := p.ClientIP(r); ok {
			return Outcome{ClientIP: ip, RemoteIP: remoteIP, Provider: p.Name(), Trusted: true}
		}
		return Outcome{RemoteIP: remoteIP, Provider: p.Name(), Reject: true, Reason: "invalid proxy header"}
	}
	return Outcome{RemoteIP: remoteIP, Reject: true, Reason: "no trusted provider"}
}

// containsIP reports whether raw parses as an IP inside one of nets.
// Unparseable input never matches (fail-closed for the claimant).
func containsIP(nets []*net.IPNet, raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Clip caps attacker-controlled values for log lines so one request
// can never stretch a line past n runes.
func Clip(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
