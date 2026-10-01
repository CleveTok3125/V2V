package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/wire"
)

// joinWire is the notice a connecting or departing client produces.
func joinWire(tag, name string) []byte {
	raw, err := json.Marshal(WireMessage{
		Type: "system", Time: "09:01",
		Tags: wire.WithTags(tag), Text: "09:01 [Hệ thống]: " + name,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// runFrames feeds frames through the pump and returns what reached the
// terminal. Closing the frame channel ends the pump.
func runFrames(t *testing.T, sess *Session, conn *captureConn, frames ...[]byte) string {
	t.Helper()
	go sess.runPump()
	for _, f := range frames {
		conn.frames <- f
	}
	// Give the pump time to drain before the channel closes.
	time.Sleep(50 * time.Millisecond)
	close(conn.frames)
	close(sess.Quitting)
	select {
	case <-sess.PumpDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not finish")
	}
	sess.flushOutputNow()
	return strings.Join(sess.Display.TabSys.lines, "")
}

// TestJoinNoticeHiddenWithoutShowJoin: without -j, a join/leave notice
// must not reach the terminal. It is the one notice whose visibility the
// user controls from the command line, and the server broadcasts it to
// everyone, so the client has to be the one that drops it.
func TestJoinNoticeHiddenWithoutShowJoin(t *testing.T) {
	sess := chainTestSession(t, 0)
	sess.Display.ShowJoinLeave = false
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess.Conn = conn

	got := runFrames(t, sess, conn, joinWire(wire.TagJoin, "Alice đã tham gia phòng chat!"))

	if strings.Contains(got, "Alice") {
		t.Fatalf("join notice leaked without -j: %q", got)
	}
}

// TestLeaveNoticeHiddenWithoutShowJoin covers the other half of the same
// switch, since join and leave are two tags behind one flag.
func TestLeaveNoticeHiddenWithoutShowJoin(t *testing.T) {
	sess := chainTestSession(t, 0)
	sess.Display.ShowJoinLeave = false
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess.Conn = conn

	got := runFrames(t, sess, conn, joinWire(wire.TagLeave, "Alice đã rời phòng chat."))

	if strings.Contains(got, "Alice") {
		t.Fatalf("leave notice leaked without -j: %q", got)
	}
}

// TestJoinNoticeShownWithShowJoin: the flag still works in the other
// direction, so the drop above cannot be satisfied by never showing them.
func TestJoinNoticeShownWithShowJoin(t *testing.T) {
	sess := chainTestSession(t, 0)
	sess.Display.ShowJoinLeave = true
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess.Conn = conn

	got := runFrames(t, sess, conn, joinWire(wire.TagJoin, "Alice đã tham gia phòng chat!"))

	if !strings.Contains(got, "Alice") {
		t.Fatalf("join notice must show with -j: %q", got)
	}
}

// TestUntaggedJoinWordingStillRenders: pins the one way a join line reaches
// the terminal without -j. A line with no tags is not a join notice as far
// as the client is concerned — nothing marks it as one — so a record from a
// pre-tag build (or a pre-tag server) renders. This is the documented cost
// of having no text fallback, and the reason a deploy takes an empty
// history.
func TestUntaggedJoinWordingStillRenders(t *testing.T) {
	sess := chainTestSession(t, 0)
	sess.Display.ShowJoinLeave = false
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess.Conn = conn

	raw, err := json.Marshal(WireMessage{
		Type: "system", Time: "09:01",
		Text: "09:01 [Hệ thống]: Alice đã tham gia phòng chat!",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := runFrames(t, sess, conn, raw)

	if !strings.Contains(got, "Alice") {
		t.Fatalf("an untagged system line must render as an ordinary notice: %q", got)
	}
}

// TestReplayMarkerArmsWindowState: a marker only counts if it actually
// raises the replay window state. The pump used to recognise a plain marker
// string; the recover tests still fed one after markers became wires and
// kept passing, because they assert paging rather than window state — so a
// marker that did nothing looked the same as one that worked.
//
// InSync in particular drives the fork check and the echo stash, so a
// marker that fails to arm it disables both silently.
func TestReplayMarkerArmsWindowState(t *testing.T) {
	sess := chainTestSession(t, 0)
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess.Conn = conn
	conn.frames <- []byte(`{"type":"history_info","min_seq":1,"max_seq":1,"min_height":1,"max_height":1,"count":1}`)
	// A header alone leaves the window raised; a footer would clear it again,
	// so the state is read with the window still open.
	conn.frames <- mustJSON(t, replayMarker(wire.WithTags(wire.TagHistoryBegin), "--- Lịch sử chat gần đây ---"))

	go sess.runPump()
	time.Sleep(50 * time.Millisecond)
	sess.Display.DisplayMu.Lock()
	armed := sess.Chain.InSync
	sess.Display.DisplayMu.Unlock()
	close(conn.frames)
	close(sess.Quitting)
	<-sess.PumpDone

	if !armed {
		t.Fatal("a tagged marker must arm the replay window")
	}
}

// TestPlainMarkerNoLongerArmsWindowState: the counterpart. A marker string
// is not a marker any more, and a mismatched binary pair is the only way to
// produce one, so this pins the documented consequence instead of leaving it
// to be discovered in production.
func TestPlainMarkerNoLongerArmsWindowState(t *testing.T) {
	sess := chainTestSession(t, 0)
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess.Conn = conn
	conn.frames <- []byte("--- Lịch sử chat gần đây ---\n")
	conn.frames <- mustJSON(t, HistorySync{Type: "history_sync", Direction: "after", Total: 0, Sent: 0})

	go sess.runPump()
	time.Sleep(50 * time.Millisecond)
	sess.Display.DisplayMu.Lock()
	armed := sess.Chain.InSync
	sess.Display.DisplayMu.Unlock()
	close(conn.frames)
	close(sess.Quitting)
	<-sess.PumpDone

	if armed {
		t.Fatal("a plain marker string must not arm the replay window")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
