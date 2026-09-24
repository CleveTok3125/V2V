package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
)

// Observe helpers feed the behavior engine with metadata only. Every
// helper is nil-safe: without a Behavior engine (tests, disabled
// feature) they cost one branch.
func (s *ChatServer) observeConnect(ip, name string, kind behavior.IdentityKind) {
	if s.Behavior == nil {
		return
	}
	s.Behavior.ObserveConnect(ip, name, kind, time.Now())
}

func (s *ChatServer) observeMessage(ip string) {
	if s.Behavior == nil {
		return
	}
	s.Behavior.ObserveMessage(ip, time.Now())
}

func (s *ChatServer) observeHistory(ip string) {
	if s.Behavior == nil {
		return
	}
	s.Behavior.ObserveHistory(ip, time.Now())
}

func (s *ChatServer) observeHTTP(ip, class string, status int) {
	if s.Behavior == nil {
		return
	}
	s.Behavior.ObserveHTTP(ip, class, status, time.Now())
}

func (s *ChatServer) observeErr(ip, code string) {
	if s.Behavior == nil {
		return
	}
	s.Behavior.ObserveErr(ip, code, time.Now())
}

func (s *ChatServer) observeDisconnect(ip string) {
	if s.Behavior == nil {
		return
	}
	s.Behavior.ObserveDisconnect(ip, time.Now())
}

// observeAuthErr maps an authentication failure to its behavior code
// from the auth_error: prefix the sentinels already carry.
func (s *ChatServer) observeAuthErr(ip string, err error) {
	if err == nil {
		return
	}
	const prefix = "auth_error: "
	msg := err.Error()
	code := "auth:fail"
	if i := strings.Index(msg, prefix); i >= 0 {
		code = "auth:" + msg[i+len(prefix):]
	}
	s.observeErr(ip, code)
}

// statusWriter captures the response status for behavior observe.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// instrumentHTTP records class/status/timing metadata (never query or
// body) for behavior scoring around a handler.
func (s *ChatServer) instrumentHTTP(class string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next(sw, r)
		if out := resolveClientIP(r); !out.Reject {
			s.observeHTTP(out.ClientIP, class, sw.status)
		}
	}
}

// sessionIdentityKind ranks the session authentication for scoring:
// minted keys outrank tripcodes outrank guests.
func sessionIdentityKind(session *ClientSession) behavior.IdentityKind {
	if session.IdentityPub != "" || session.AuthType == "passkey" {
		return behavior.IdKey
	}
	if session.TripPub != "" {
		return behavior.IdTrip
	}
	return behavior.IdGuest
}
