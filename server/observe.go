package main

import (
	"bufio"
	"errors"
	"net"
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
	now := time.Now()
	a := Cfg.Abuse.Load()
	if s.Screener == nil || a == nil || a.Behavior == nil || a.Behavior.ScoreEveryNMsgs < 1 {
		s.Behavior.ObserveMessage(ip, now)
		return
	}
	// One critical section for both the record and the budget check:
	// this is the chat hot path.
	if s.Behavior.ObserveMessageAndDue(ip, now, a.Behavior.ScoreEveryNMsgs) {
		// Flag the IP for the next scheduler tick once it has produced
		// ScoreEveryNMsgs messages since its last score, so short
		// bursty sessions are re-scored instead of evaluated once at
		// connect time.
		s.Screener.Defer(ip, now)
	}
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

// Compile-time guard: every wrapped route (including the WS upgrade
// on "/") must keep hijacking.
var _ http.Hijacker = (*statusWriter)(nil)

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack passes through so wrapped handlers (notably the WS upgrade
// on "/") keep working: embedding the interface alone does not
// promote the optional Hijacker method.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack unsupported")
}

// instrumentHTTP records class/status/timing metadata (never query or
// body) for behavior scoring around a handler.
func (s *ChatServer) instrumentHTTP(class string, next http.HandlerFunc) http.HandlerFunc {
	return s.instrumentHTTPClass(func(*http.Request) string { return class }, next)
}

// instrumentHTTPClass is instrumentHTTP with a per-request class: the
// WS branch of "/" returns "" so ServeWS records join_ws itself (the
// blocking handler would otherwise report at disconnect).
func (s *ChatServer) instrumentHTTPClass(classOf func(*http.Request) string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next(sw, r)
		class := classOf(r)
		if class == "" {
			return
		}
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
