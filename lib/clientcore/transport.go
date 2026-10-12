package clientcore

import "net/http"

// Conn is the small WebSocket surface the core uses. The caller wraps
// whatever socket library it has: gorilla on the desktop, the browser's
// native WebSocket under wasm. The core never dials on its own.
type Conn interface {
	ReadJSON(v any) error
	WriteJSON(v any) error
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	SetReadLimit(n int64)
	Close() error
}

// Transport opens a socket. A non-nil response is the failed upgrade, which
// the core inspects for a join-gate challenge before giving up.
type Transport interface {
	Dial(url string) (Conn, *http.Response, error)
}

// Gate is the join gate: the proof-of-work challenge a server may demand
// before it upgrades. Pass returns a lease pass the caller already holds —
// the browser client fetches one before dialing, because the browser hides
// the 429. Solve runs the challenge for a ws(s) URL and a pinned server key.
// A false return means the caller gave up, and the dial reports the original
// error. Both run in the caller, which owns the solver and its budget.
type Gate interface {
	Pass(url string) string
	Solve(wsURL, pin string) (string, bool)
}

// DialConfig is what the dial needs to know about the caller.
type DialConfig struct {
	// KeyFile is the identity file whose pinned server key the gate offer
	// is verified against. Empty, or an encrypted file, means
	// trust-on-first-use: unlocking would prompt before the dial.
	KeyFile string
	// ReadLimit caps one inbound frame, in bytes. Zero (or less) selects
	// defaultReadLimit: a gorilla-backed Conn reads unbounded at zero, and
	// an unbounded frame is a server-controlled allocation.
	ReadLimit int64
	// MaxAttempts bounds join-gate retries: one initial dial plus gate
	// passes. A persistent 429 means the gate itself is failing, not a
	// solvable challenge.
	MaxAttempts int
}

// DialEventKind is what a dial reports to the human. The core carries no
// user-facing text: the caller decides the wording, and the language.
type DialEventKind int

const (
	// DialUpgraded: the server refused ws:// and demanded wss://.
	DialUpgraded DialEventKind = iota
	// DialGateRequired: the server demanded a proof-of-work pass.
	DialGateRequired
	// DialFailed: the dial failed. Err is the transport error, Status the
	// HTTP status of the failed upgrade (0 when there was no response), and
	// Body the response body, already bounded and neutralized.
	DialFailed
)

// DialEvent carries what happened. URL is set for DialUpgraded.
type DialEvent struct {
	Kind   DialEventKind
	URL    string
	Err    error
	Status int
	Body   string
}

// DialOutput receives dial events.
type DialOutput interface {
	OnDial(e DialEvent)
}

// Dialer is the core's connection setup: dial, upgrade to TLS when the
// server asks, and solve the join gate when it demands one.
type Dialer struct {
	Transport Transport
	Gate      Gate
	Config    DialConfig
	Output    DialOutput
}
