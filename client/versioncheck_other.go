//go:build !js

package main

import (
	"net/http"
	"os"
	"time"
)

// versionHTTPClient builds the client for the pre-dial version check.
// It resolves the session proxy exactly once through the shared cache
// (the same one dialWS reuses), so the check rides tor instead of
// always going direct. Direct keeps the 5s fail-fast bound; an
// explicit proxy gets 30s because circuit setup is slow.
func versionHTTPClient() (*http.Client, error) {
	if !proxyResolved {
		proxyResolved = true
		resolvedProxy, proxyResolveErr = resolveProxy(os.Stdin)
	}
	if proxyResolveErr != nil {
		return nil, proxyResolveErr
	}
	if resolvedProxy == nil {
		return &http.Client{Timeout: 5 * time.Second}, nil
	}
	if resolvedProxy.Scheme == "socks5" {
		return &http.Client{
			Transport: &http.Transport{DialContext: socks5NetDialer(resolvedProxy)},
			Timeout:   30 * time.Second,
		}, nil
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(resolvedProxy.dialURL())},
		Timeout:   30 * time.Second,
	}, nil
}
