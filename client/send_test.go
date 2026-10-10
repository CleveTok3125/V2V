package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
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
	if err := sess.sendMessage("hello", 1, 1, true, 0); err != nil {
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
	err := sess.sendMessage("hello", 1, 1, true, 0)
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
	if err := sess.sendMessage("hello", 1, 1, true, 0); err != nil {
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

// assertSwapEraseCoversDrawnRows drives one placeholder -> echo swap and
// checks that the erase moves up exactly the terminal rows that were drawn
// below the placeholder's first row. The expectation is summed from the
// buffer entries themselves by an independent pass, so an accounting that
// counted entries instead of rows fails here.
func assertSwapEraseCoversDrawnRows(t *testing.T, body string, replyTo uint64) {
	t.Helper()
	sess, out := queueTestSession(t)
	sess.Username = "A#1"
	sess.Chain.WireIdx = newWireIndex(1000)
	sess.Chain.RenderCache = newRenderCache(200)
	sess.Pending.TmpSeq = 7
	if replyTo > 0 {
		// A pending reply prints its quote above the author row, so the
		// region's first row is a preview of another message.
		sess.Chain.WireIdx.put(chainWireWithText(replyTo, "tin được trả lời", 0x05))
		sess.Pending.PendingReplyTo = replyTo
	}

	phRows, phScreenRows, shown, bufEnd := sess.renderPlaceholder(body, 1)
	if !shown || phRows == 0 {
		t.Fatalf("placeholder did not render: rows=%d shown=%v", phRows, shown)
	}
	phEntries := append([]string{}, sess.Display.TabChat.lines[bufEnd-phRows:bufEnd]...)
	sess.Pending.PendingPlaceholders = append(sess.Pending.PendingPlaceholders, pendingMsg{
		text: body, rows: phRows, screenRows: phScreenRows, shown: shown, bufEnd: bufEnd,
		tmpID: sess.Pending.TmpSeq, replyTo: replyTo,
	})
	// Everything queued so far must be on the wire before the measurement,
	// or the input-line wipe would be counted as part of the echo.
	sess.flushOutputNow()
	beforeEcho := out.Len()

	echo := WireMessage{
		Type: "chat", Time: "12:00", DisplayName: "A#1", Text: body,
		TmpID: sess.Pending.TmpSeq, ChainHeight: 1, ChainHash: "ab", ChainVer: 2,
		ReplyTo: replyTo,
	}
	sess.Display.DisplayMu.Lock()
	sess.renderChatBlock(echo)
	echoEntries := append([]string{}, sess.Display.TabChat.lines[bufEnd:]...)
	sess.consumeEchoLocked(echo, false)
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	drawn := out.String()[beforeEcho:]
	if !strings.Contains(drawn, "\x1b[") {
		t.Fatalf("no erase sequence was emitted, so the placeholder is still on screen:\n%q", drawn)
	}
	moved, err := strconv.Atoi(regexp.MustCompile(`\x1b\[(\d+)A`).FindStringSubmatch(drawn)[1])
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, entry := range append(append([]string{}, phEntries...), echoEntries...) {
		want += estimateRows(ansi.Strip(entry), termWidth())
	}
	if moved != want {
		t.Fatalf("erase moved up %d rows, want %d (placeholder %d entries / %d rows, echo %d entries)\nbody=%q",
			moved, want, phRows, phScreenRows, len(echoEntries), body)
	}
}

// TestPlaceholderEraseCoversWhatWasDrawn pins the arithmetic the echo swap
// depends on: the erase must move up exactly as many terminal rows as were
// drawn. Two shapes broke it. A message block is one buffer entry however
// many lines it has, so counting intervening entries moved the cursor up
// short and left the tail on screen; and the region guard demanded the
// pending mark on every row, which the body's continuation rows never
// carried, so a multi-line placeholder was never erased at all. A third
// shape reached the same guard: a pending reply prints its quote first, and
// the quote is a preview of another message, so it carries no mark.
func TestPlaceholderEraseCoversWhatWasDrawn(t *testing.T) {
	t.Run("two lines", func(t *testing.T) { assertSwapEraseCoversDrawnRows(t, "line1\nline2", 0) })
	t.Run("one line", func(t *testing.T) { assertSwapEraseCoversDrawnRows(t, "single-line", 0) })
	t.Run("line long enough to wrap", func(t *testing.T) {
		assertSwapEraseCoversDrawnRows(t, strings.Repeat("x", 200), 0)
	})
	// The quote carries no pending mark of its own, so a guard that only
	// reads the region's first row would refuse to erase this placeholder
	// and leave it on screen for good.
	t.Run("with a reply quote", func(t *testing.T) {
		assertSwapEraseCoversDrawnRows(t, "trả lời", 5)
	})
}
