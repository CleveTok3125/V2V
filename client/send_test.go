package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type stubConn struct {
	writeErr error
	wrote    any
}

func (c *stubConn) ReadJSON(any) error                { return io.EOF }
func (c *stubConn) WriteJSON(v any) error             { c.wrote = v; return c.writeErr }
func (c *stubConn) ReadMessage() (int, []byte, error) { return 0, nil, io.EOF }
func (c *stubConn) WriteMessage(int, []byte) error    { return nil }
func (c *stubConn) SetReadLimit(int64)                {}
func (c *stubConn) Close() error                      { return nil }

// TestSendMessagePlainLoopback exercises the moved send path over a real
// loopback websocket: the server must receive the envelope, the
// placeholder must be tracked, and the one-shot reply target cleared.
func TestSendMessagePlainLoopback(t *testing.T) {
	got := make(chan PlainMessage, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var pm PlainMessage
		if err := c.ReadJSON(&pm); err != nil {
			return
		}
		got <- pm
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws://"+strings.TrimPrefix(srv.URL, "http://"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	sess := NewSession()
	sess.Conn = conn
	sess.Username = "u"
	sess.Display.Out = io.Discard
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Pending.PendingReplyTo = 7
	if err := sess.sendMessage("hello", 1, true, 0); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	select {
	case pm := <-got:
		if pm.Text != "hello" || pm.TmpID == 0 || pm.ReplyTo != 7 {
			t.Fatalf("server got %+v", pm)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server received nothing")
	}
	if len(sess.Pending.PendingPlaceholders) != 1 ||
		sess.Pending.PendingPlaceholders[0].text != "hello" {
		t.Fatalf("placeholder not tracked: %+v", sess.Pending.PendingPlaceholders)
	}
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatal("one-shot reply target not cleared")
	}
}

// TestCollectBodyPassthrough covers the non-fence fast path and the
// guard happy path without a terminal.
func TestCollectBodyPassthrough(t *testing.T) {
	sess := NewSession()
	body, lines, ok := sess.collectBody("hi")
	if !ok || body != "hi" || lines != 1 {
		t.Fatalf("got %q,%d,%v", body, lines, ok)
	}
	if !sess.checkSendGuards("hi") {
		t.Fatal("simple greeting blocked")
	}
	// Empty input is dropped by the input loop before guards run.
}

func tripSessionForSend(t *testing.T, conn wsConn) *Session {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession()
	sess.Conn = conn
	sess.TripPriv = priv
	sess.TripPub = pub
	sess.Username = "u"
	sess.Display.Out = io.Discard
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabChat = newTabBuffer(100, 100000)
	return sess
}

func TestSendMessageTripWriteJSONError(t *testing.T) {
	conn := &stubConn{writeErr: errors.New("conn closed")}
	sess := tripSessionForSend(t, conn)
	sess.TripSeq = 5
	sess.Pending.TmpSeq = 10
	prev := append([]byte(nil), sess.TripPrev...)
	sess.Pending.PendingReplyTo = 7
	err := sess.sendMessage("hello", 1, true, 0)
	if err == nil {
		t.Fatal("WriteJSON fail must return err")
	}
	if sess.TripSeq != 5 {
		t.Fatalf("TripSeq=%d want rollback to 5", sess.TripSeq)
	}
	if sess.Pending.TmpSeq != 10 {
		t.Fatalf("TmpSeq=%d want rollback to 10", sess.Pending.TmpSeq)
	}
	if string(sess.TripPrev) != string(prev) {
		t.Fatal("TripPrev not rolled back")
	}
	if len(sess.Pending.PendingPlaceholders) != 0 {
		t.Fatalf("placeholder tracked on fail: %+v", sess.Pending.PendingPlaceholders)
	}
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatal("one-shot reply target not cleared")
	}
}

func TestSendMessageTripWriteJSONOK(t *testing.T) {
	conn := &stubConn{}
	sess := tripSessionForSend(t, conn)
	if err := sess.sendMessage("hello", 1, true, 0); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	tm, ok := conn.wrote.(TripMessage)
	if !ok || tm.Text != "hello" || tm.TmpID == 0 {
		t.Fatalf("wrote %+v", conn.wrote)
	}
	if len(sess.Pending.PendingPlaceholders) != 1 {
		t.Fatalf("placeholder not tracked: %+v", sess.Pending.PendingPlaceholders)
	}
}
