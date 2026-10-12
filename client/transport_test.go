//go:build !js

package main

import (
	"errors"
	"io"
	"testing"

	"github.com/CleveTok3125/V2V/lib/clientcore"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// challengeConn answers one ReadJSON with a scripted packet or failure.
type challengeConn struct {
	packet wire.AuthPacket
	err    error
}

func (c *challengeConn) ReadJSON(v any) error {
	if c.err != nil {
		return c.err
	}
	*(v.(*wire.AuthPacket)) = c.packet
	return nil
}
func (c *challengeConn) WriteJSON(any) error               { return nil }
func (c *challengeConn) ReadMessage() (int, []byte, error) { return 0, nil, io.EOF }
func (c *challengeConn) WriteMessage(int, []byte) error    { return nil }
func (c *challengeConn) SetReadLimit(int64)                {}
func (c *challengeConn) Close() error                      { return nil }

// TestReadChallengeWordsAProtocolMismatch pins the line shown when the
// server's first frame is not an auth_challenge. The core reports a sentinel,
// so the wording lives here and must not drift.
func TestReadChallengeWordsAProtocolMismatch(t *testing.T) {
	sess := NewSession()
	sess.Conn = &challengeConn{packet: wire.AuthPacket{Type: "chat"}}
	out := captureStdout(t, func() {
		if _, err := sess.readChallenge(); !errors.Is(err, clientcore.ErrNotAuthChallenge) {
			t.Errorf("err = %v, want ErrNotAuthChallenge", err)
		}
	})
	if out != "❌ Lỗi: Server không gửi Auth Challenge hợp lệ.\n" {
		t.Fatalf("out = %q", out)
	}
}

// A dead socket is not a protocol mismatch: the dial path already reports the
// connection failure, so this must stay silent.
func TestReadChallengeStaysSilentOnASocketError(t *testing.T) {
	sess := NewSession()
	sess.Conn = &challengeConn{err: io.EOF}
	out := captureStdout(t, func() {
		if _, err := sess.readChallenge(); !errors.Is(err, io.EOF) {
			t.Errorf("err = %v, want the socket error", err)
		}
	})
	if out != "" {
		t.Fatalf("out = %q, want nothing", out)
	}
}
