package main

import (
	"net/http"
	"strings"
)

// corsOrigin returns the request Origin when ALLOWED_ORIGINS lists it,
// matching the WebSocket origin check.
func corsOrigin(r *http.Request) (string, bool) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return "", false
	}
	for _, allowed := range Cfg.Static.AllowedOrigins {
		if origin == strings.TrimSpace(allowed) {
			return origin, true
		}
	}
	return "", false
}

// corsMiddleware answers preflights and stamps CORS headers for ALLOWED_ORIGINS.
//
// Allow-Credentials stays unset because proof travels in the body.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, ok := corsOrigin(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		// Caches must key on Origin: the header differs per origin.
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, X-V2V-Pass")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
