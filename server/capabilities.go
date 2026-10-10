package main

import (
	"encoding/json"
	"net/http"
)

// handleAPICapabilities answers what this server can do that a client
// cannot see from the protocol alone. It is separate from /api/version on
// purpose: that endpoint carries the release stamp and is part of the
// version policy a client checks before dialing, so it must not grow
// unrelated fields.
//
// No-store, same style as the other /api meta endpoints.
func handleAPICapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"passkey": WAConfig.Enabled,
	})
}
