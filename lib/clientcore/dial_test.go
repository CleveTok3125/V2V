package clientcore

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/lib/wire"
)

// dialBodyText bounds the read and neutralizes escapes in a failed
// dial's response body.
func TestDialBodyText(t *testing.T) {
	if got := DialBodyText(nil); got != "" {
		t.Fatalf("nil response = %q, want empty", got)
	}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(
		"  nope\x1b]52;c;AAAA\x1b\\  "))}
	if got := DialBodyText(resp); strings.Contains(got, "52;c") || strings.Contains(got, "\x1b") {
		t.Fatalf("escape leaked: %q", got)
	} else if got != "nope" {
		t.Fatalf("got %q, want %q", got, "nope")
	}
	big := &http.Response{Body: io.NopCloser(strings.NewReader(
		strings.Repeat("x", dialErrorBody+4096)))}
	if got := DialBodyText(big); len(got) != dialErrorBody {
		t.Fatalf("body not capped: len=%d want=%d", len(got), dialErrorBody)
	}
}

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

func TestReadChallengeAcceptsTheChallenge(t *testing.T) {
	conn := &challengeConn{packet: wire.AuthPacket{Type: "auth_challenge", Nonce: "n1"}}
	got, err := ReadChallenge(conn)
	if err != nil || got.Nonce != "n1" {
		t.Fatalf("ReadChallenge = %+v, %v", got, err)
	}
}

// A frame of another type is a protocol mismatch, and a caller has to be able
// to tell it apart from a dead socket to report it as one.
func TestReadChallengeFlagsANonChallengeFrame(t *testing.T) {
	conn := &challengeConn{packet: wire.AuthPacket{Type: "chat"}}
	if _, err := ReadChallenge(conn); !errors.Is(err, ErrNotAuthChallenge) {
		t.Fatalf("err = %v, want ErrNotAuthChallenge", err)
	}
}

func TestReadChallengeKeepsASocketErrorDistinct(t *testing.T) {
	boom := errors.New("conn reset")
	conn := &challengeConn{err: boom}
	_, err := ReadChallenge(conn)
	if !errors.Is(err, boom) || errors.Is(err, ErrNotAuthChallenge) {
		t.Fatalf("err = %v, want the socket error alone", err)
	}
}
