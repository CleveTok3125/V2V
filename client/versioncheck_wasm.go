//go:build js

package main

import (
	"net/http"
	"time"
)

// versionHTTPClient on wasm is a direct client: the browser owns
// proxying there, and checkServerVersion returns before dialing
// anyway (paired with its serving server).
func versionHTTPClient() (*http.Client, error) {
	return &http.Client{Timeout: 5 * time.Second}, nil
}
