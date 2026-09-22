package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/CleveTok3125/V2V/internal/trustedproxy"
)

// DefaultTrustedProxyDir is the conventional trust-file directory,
// mirroring config/roles.json. Only the listed chain members load
// their "<name>.txt" from here.
const DefaultTrustedProxyDir = "config/trustedproxy"

// ProxyChain is the boot-built resolution chain. Tests that never
// call initProxyChain resolve as direct (RemoteAddr), which matches
// the old behavior for headerless requests.
var ProxyChain *trustedproxy.Chain

// proxyChainOrDirect returns the active chain, falling back to a
// header-ignoring direct chain when the server never built one
// (unit tests). Production always builds the real chain at boot.
func proxyChainOrDirect() *trustedproxy.Chain {
	if ProxyChain != nil {
		return ProxyChain
	}
	chain, err := trustedproxy.NewChain([]string{"none"}, nil)
	if err != nil {
		panic("trustedproxy: none chain must always build: " + err.Error())
	}
	return chain
}

// resolveClientIP maps one request to the real client IP through the
// active chain. Either Outcome.ClientIP is set or Outcome.Reject is
// true (strict chain with no claimant).
func resolveClientIP(r *http.Request) trustedproxy.Outcome {
	return proxyChainOrDirect().Resolve(r)
}

// getClientIP returns the resolved client IP for rate limiting, auth
// binding and logging. Untrusted proxy headers never reach here:
// strict chains reject before this is called.
//
// Note: a rejected request resolves to the empty string. Handlers that
// call getClientIP directly (rather than going through ServeWS) must be
// guarded with rejectUntrustedProxy first, or every rejected request
// shares one rate-limit bucket.
func getClientIP(r *http.Request) string {
	return resolveClientIP(r).ClientIP
}

// rejectUntrustedProxy answers 403 and reports true when a strict chain
// refuses the request (no trusted claimant). It mirrors the ServeWS gate
// for non-WebSocket endpoints that otherwise resolve an empty client IP.
func rejectUntrustedProxy(w http.ResponseWriter, r *http.Request) bool {
	outcome := resolveClientIP(r)
	if !outcome.Reject {
		return false
	}
	logWarnf("⛔ [PROXY] Reject %s (%s): %s", trustedproxy.Clip(outcome.RemoteIP, 200), outcome.Reason, proxyHeadersForLog(r))
	http.Error(w, "Untrusted proxy.", http.StatusForbidden)
	return true
}

// initOnionTrust loads the onion-hop allowlist from onion.txt next to the
// other trust files. A missing file means loopback-only (tolerated with a
// warning); a malformed or world-writable file fails the boot.
func initOnionTrust(dir string) ([]*net.IPNet, error) {
	path := filepath.Join(dir, "onion.txt")
	set, err := trustedproxy.LoadTrustFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logWarnf("⚠️ Onion: %s missing; only loopback hops accepted", path)
			return nil, nil
		}
		return nil, err
	}
	logInfof("🧅 Onion: %d trusted hops from %s", len(set.Nets), path)
	return set.Nets, nil
}

// initProxyChain loads per-module trust files and builds the
// resolution chain, printing the explicit boot report so the active
// trust is always visible in the logs. Any failure is fatal: trust
// must never silently degrade.
func initProxyChain(dir string, names []string) (*trustedproxy.Chain, error) {
	required := map[string]bool{}
	for _, name := range names {
		if build, ok := trustedproxy.Lookup(name); ok && build(nil).NeedsFile() {
			required[name] = true
		}
	}
	sets, extraWarn, err := trustedproxy.LoadTrustDir(dir, names, required)
	if err != nil {
		return nil, err
	}
	nets := make(map[string][]*net.IPNet, len(sets))
	for name, set := range sets {
		nets[name] = set.Nets
	}
	chain, err := trustedproxy.NewChain(names, nets)
	if err != nil {
		return nil, err
	}
	logInfof("🛡️ Trusted proxy: providers=[%s]", strings.Join(names, ","))
	for _, name := range names {
		set := sets[name]
		if set.Missing {
			logInfof("🛡️ Trusted proxy: %s: no file (%s), empty set", name, set.Path)
		} else {
			logInfof("🛡️ Trusted proxy: %s: %d ranges from %s", name, len(set.Nets), set.Path)
		}
	}
	allowsDirect := false
	for _, name := range names {
		if name == "none" || name == "direct" {
			allowsDirect = true
		}
	}
	if allowsDirect {
		logInfof("🛡️ Trusted proxy: direct connections allowed (WARN per connection)")
	} else {
		logInfof("🛡️ Trusted proxy: direct connections rejected (403)")
	}
	if extraWarn != "" {
		logWarnf("⚠️ Trusted proxy: %s", extraWarn)
	}
	return chain, nil
}

// proxyHeadersForLog renders the received proxy headers for the
// connect log, clipped so one request stays one line.
func proxyHeadersForLog(r *http.Request) string {
	parts := []string{}
	for _, h := range []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP"} {
		if v := r.Header.Get(h); v != "" {
			parts = append(parts, h+"="+trustedproxy.Clip(v, 200))
		}
	}
	return strings.Join(parts, " ")
}
