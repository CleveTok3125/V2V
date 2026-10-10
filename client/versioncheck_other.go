//go:build !js

package main

import (
	"net/http"
	"os"
	"time"
)

// serverHTTPClient is the client every request to the server uses. Going
// through one builder is what keeps the session proxy from being skipped:
// a bare http.Client falls back to http.DefaultTransport and therefore to
// the HTTP_PROXY environment variables, which resolveProxy never writes —
// so such a client connects from the real address while the rest of the
// session rides the proxy.
//
// Direct keeps the 5s fail-fast bound; an explicit proxy gets 30s because
// circuit setup is slow.
func serverHTTPClient() (*http.Client, error) {
	tr, err := serverHTTPTransport()
	if err != nil {
		return nil, err
	}
	timeout := 5 * time.Second
	if tr != nil {
		timeout = 30 * time.Second
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// serverHTTPTransport resolves the session proxy exactly once through the
// shared cache (the same one dialWS reuses) and returns the transport that
// carries a request to the server. A nil transport means "no proxy", which
// callers pass to http.Client unchanged so it keeps the default transport.
func serverHTTPTransport() (http.RoundTripper, error) {
	if !proxyResolved {
		proxyResolved = true
		resolvedProxy, proxyResolveErr = resolveProxy(os.Stdin)
	}
	if proxyResolveErr != nil {
		return nil, proxyResolveErr
	}
	if resolvedProxy == nil {
		return nil, nil
	}
	if resolvedProxy.Scheme == "socks5" {
		return &http.Transport{DialContext: socks5NetDialer(resolvedProxy)}, nil
	}
	return &http.Transport{Proxy: http.ProxyURL(resolvedProxy.dialURL())}, nil
}
