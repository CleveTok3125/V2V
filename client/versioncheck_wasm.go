//go:build js

package main

import (
	"net/http"
	"time"
)

// serverHTTPClient on wasm is a direct client: the browser owns proxying
// there, and checkServerVersion returns before dialing anyway (paired with
// its serving server).
func serverHTTPClient() (*http.Client, error) {
	return &http.Client{Timeout: 5 * time.Second}, nil
}

// serverHTTPTransport is nil on wasm for the same reason: there is no
// transport to configure when the browser owns the network.
func serverHTTPTransport() (http.RoundTripper, error) { return nil, nil }
