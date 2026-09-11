package main

import (
	"encoding/json"
	"net/http"
)

// Version is the server release stamp. Release builds inject it via
// -X main.Version (Makefile LDFLAGS); dev builds use dev-HASH[-dirty];
// otherwise it stays "dev", exactly like the client binaries.
var Version = "dev"

// handleAPIVersion is the static version endpoint clients query before
// dialing: {"version": "..."}. No-store, same style as /api/server_pubkey.
func handleAPIVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"version": Version})
}
