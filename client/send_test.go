package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
	sess.Out = io.Discard
	sess.Term = &fakeTerm{}
	sess.TabChat = newTabBuffer(100, 100000)
	sess.PendingReplyTo = 7
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
	if len(sess.PendingPlaceholders) != 1 ||
		sess.PendingPlaceholders[0].text != "hello" {
		t.Fatalf("placeholder not tracked: %+v", sess.PendingPlaceholders)
	}
	if sess.PendingReplyTo != 0 {
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
