package main

// The client's side of lib/clientcore: the socket, the join gate, and the
// wording of what the dial reports.

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/CleveTok3125/V2V/lib/clientcore"
)

// wsTransport is the core's Transport over the client's dialer.
type wsTransport struct{}

func (wsTransport) Dial(url string) (clientcore.Conn, *http.Response, error) { return dialWS(url) }

// cliGate is the core's Gate over the client's join-gate solver.
type cliGate struct{}

func (cliGate) Pass(url string) string { return preDialPass(url) }

func (cliGate) Solve(wsURL, pin string) (string, bool) {
	return clientcore.RunGateFlow(clientcore.GateHTTPBase(wsURL), gateConfig(pin))
}

// cliDialOutput renders the core's dial events in the client's language.
type cliDialOutput struct{}

func (cliDialOutput) OnDial(e clientcore.DialEvent) {
	switch e.Kind {
	case clientcore.DialUpgraded:
		fmt.Printf("🔒 Server yêu cầu wss://, đang thử lại với %s…\n", e.URL)
	case clientcore.DialGateRequired:
		fmt.Println("🧩 Server yêu cầu vượt cửa PoW, đang giải…")
	case clientcore.DialFailed:
		fmt.Println("❌ Không thể kết nối:", e.Err)
		if e.Status != 0 {
			fmt.Printf("👉 HTTP Status Code: %d\n", e.Status)
		}
		if e.Body != "" {
			fmt.Printf("📦 Nội dung phản hồi: %s\n", e.Body)
		}
	}
}

// maxGateAttempts bounds join-gate retries: one initial dial plus gate
// passes. A persistent 429 means the gate itself is failing, not a solvable
// challenge.
const maxGateAttempts = 3

// newDialer is the core Dialer the session bootstraps with.
func newDialer() clientcore.Dialer {
	return clientcore.Dialer{
		Transport: wsTransport{},
		Gate:      cliGate{},
		Config: clientcore.DialConfig{
			KeyFile:     CLI.KeyFile,
			ReadLimit:   clientReadLimit(),
			MaxAttempts: maxGateAttempts,
		},
		Output: cliDialOutput{},
	}
}

// readChallenge reads the opening packet and reports a protocol mismatch in
// the client's words. The core returns a sentinel rather than a message, so
// a third-party client is free to say it differently.
func (s *Session) readChallenge() (AuthPacket, error) {
	challenge, err := clientcore.ReadChallenge(s.Conn)
	if errors.Is(err, clientcore.ErrNotAuthChallenge) {
		fmt.Println("❌ Lỗi: Server không gửi Auth Challenge hợp lệ.")
	}
	return challenge, err
}
