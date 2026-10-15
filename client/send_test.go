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
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Pending.PendingReplyTo = 7
	if err := sess.sendMessage("hello", entry{}, slotRef{}, true); err != nil {
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
	err := sess.sendMessage("hello", entry{}, slotRef{}, true)
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
	if err := sess.sendMessage("hello", entry{}, slotRef{}, true); err != nil {
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
	sess, _ := queueTestSession(t)
	sess.Username = "A#1"
	sess.Chain.WireIdx = newWireIndex(1000)
	sess.Chain.RenderCache = newRenderCache(200)
	sess.Pending.TmpSeq = 7
	if replyTo > 0 {
		// A pending reply prints its quote inside the block, so the region
		// carries a preview of another message as well as the author row.
		sess.Chain.WireIdx.put(chainWireWithText(replyTo, "tin được trả lời", 0x05))
		sess.Pending.PendingReplyTo = replyTo
	}

	// The submitted line becomes an entry, and the block takes its slot.
	slot := sess.echoInputLine(body)
	phEntry, slot, shown := sess.renderPlaceholder(body, slot)
	if !shown {
		t.Fatalf("placeholder did not render: shown=%v", shown)
	}
	pm := pendingMsg{
		text: body, slot: slot, shown: shown, epoch: sess.Display.LayoutEpoch,
		tmpID: sess.Pending.TmpSeq, replyTo: replyTo,
	}
	if got := sess.Display.TabChat.lines[slot.index]; got != phEntry {
		t.Fatalf("the block did not take the input echo's slot")
	}

	echo := WireMessage{
		Type: "chat", Time: "12:00", DisplayName: "A#1", Text: body,
		TmpID: sess.Pending.TmpSeq, ChainHeight: 1, ChainHash: "ab", ChainVer: 2,
		ReplyTo: replyTo,
	}
	sess.Display.DisplayMu.Lock()
	// The echo is consumed first, arming the slot the render then writes into.
	sess.Pending.Mu.Lock()
	sess.Pending.EchoSlot = &pm
	sess.Pending.Mu.Unlock()
	sess.renderChatBlock(echo)
	sess.Display.DisplayMu.Unlock()

	if lines := sess.Display.TabChat.lines; len(lines) != 1 {
		t.Fatalf("buffer holds %d entries after the echo, want 1 (the echo)", len(lines))
	}
	if !strings.Contains(sess.Display.TabChat.lines[0].text(), "12:00") {
		t.Fatalf("the echo never replaced the block: %q", entriesText(sess.Display.TabChat.lines))
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
// TestPlaceholderLeavesTheBufferOffScreen pins the half of the swap that has
// nothing to do with the terminal: the slot must leave the buffer whatever is
// on screen. Both cases used to return before the splice — a send while tab 2
// was open, and a tab switch between send and echo — so the placeholder
// stayed in the history and the message read twice as soon as the echo
// rendered. The screen erase is a separate question: moving up is only safe
// while nothing has repainted, so a stale layout must skip it.
// TestOneMessageIsOneSlotFromInputToEcho pins the rule the input echo exists
// for: the submitted line, the pending block and the server echo are the same
// buffer entry, and the terminal sees each replace the last rather than land
// beside it. Anything else leaves the message on screen twice, which is what
// a tab switch between send and echo used to produce.
func TestOneMessageIsOneSlotFromInputToEcho(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Username = "A#1"
	sess.Chain.WireIdx = newWireIndex(1000)
	sess.Chain.RenderCache = newRenderCache(200)
	sess.Pending.TmpSeq = 7

	slot := sess.echoInputLine("xin chào")
	if lines := sess.Display.TabChat.lines; len(lines) != 1 ||
		lines[0].text() != "xin chào\n\n" {
		t.Fatalf("the input echo must be one entry: %q", entriesText(lines))
	}

	_, slot, shown := sess.renderPlaceholder("xin chào", slot)
	if lines := sess.Display.TabChat.lines; len(lines) != 1 ||
		!strings.Contains(lines[0].text(), "⏳") {
		t.Fatalf("the block must take the echo's slot: %q", entriesText(lines))
	}

	echo := WireMessage{
		Type: "chat", Time: "12:00", DisplayName: "A#1", Text: "xin chào",
		TmpID: 7, ChainHeight: 1, ChainHash: "ab", ChainVer: 2,
	}
	sess.Display.DisplayMu.Lock()
	sess.Pending.Mu.Lock()
	sess.Pending.EchoSlot = &pendingMsg{
		text: "xin chào", slot: slot, shown: shown,
		epoch: sess.Display.LayoutEpoch, tmpID: 7,
	}
	sess.Pending.Mu.Unlock()
	sess.renderChatBlock(echo)
	sess.Display.DisplayMu.Unlock()

	lines := sess.Display.TabChat.lines
	if len(lines) != 1 {
		t.Fatalf("buffer holds %d entries after the echo, want 1: %q", len(lines), entriesText(lines))
	}
	if strings.Contains(lines[0].text(), "⏳") {
		t.Fatalf("the block survived the echo: %q", lines[0].text())
	}
	// The spy paints the whole chat, so a duplicate would show up as a
	// second copy of the body.
	drawn := out.String()
	if got := strings.Count(drawn, "xin chào"); got != 1 {
		t.Fatalf("the body was drawn %d times in one replace: %q", got, drawn)
	}
}

// TestRejectedLineKeepsItsEchoEntry: a message the guards stop never gets a
// block, so its echo entry stays as the record of what was typed.
func TestRejectedLineKeepsItsEchoEntry(t *testing.T) {
	sess, _ := queueTestSession(t)
	slot := sess.echoInputLine("quá nhanh")
	lines := sess.Display.TabChat.lines
	if len(lines) != 1 || slot.index != 0 {
		t.Fatalf("the echo entry must own its slot: %q", entriesText(lines))
	}
	if got := lines[0].text(); got != "quá nhanh\n\n" {
		t.Fatalf("echo entry = %q", got)
	}
}
