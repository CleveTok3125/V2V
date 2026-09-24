package main

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/pow"
)

var screenPreset = pow.Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 8}

func TestScreenIssueResolve(t *testing.T) {
	sc := NewPowScreener(nil)
	conn := &websocket.Conn{}
	sc.Issue(conn, "10.0.0.1", 1, screenPreset, "salt-1", "ch-1", time.Now().Add(time.Minute))
	nonce, err := pow.Solve(screenPreset, "salt-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := sc.Resolve("ch-1", nonce)
	if !ok || got != conn {
		t.Fatal("valid solution must resolve")
	}
	if sc.IsMuted(conn) {
		t.Fatal("resolved conn must not be muted")
	}
	// Single-use: gone now.
	if _, ok := sc.Resolve("ch-1", nonce); ok {
		t.Fatal("consumed challenge must not resolve twice")
	}
}

func TestScreenWrongConsumes(t *testing.T) {
	sc := NewPowScreener(nil)
	conn := &websocket.Conn{}
	sc.Issue(conn, "10.0.0.1", 1, screenPreset, "salt-2", "ch-2", time.Now().Add(time.Minute))
	if _, ok := sc.Resolve("ch-2", 12345); ok {
		t.Fatal("wrong nonce must fail")
	}
	if _, ok := sc.Resolve("ch-2", 12345); ok {
		t.Fatal("failed challenge must be consumed")
	}
}

func TestScreenDeclineMutes(t *testing.T) {
	sc := NewPowScreener(nil)
	conn := &websocket.Conn{}
	sc.Issue(conn, "10.0.0.1", 1, screenPreset, "salt-3", "ch-3", time.Now().Add(time.Minute))
	got, ok := sc.Decline("ch-3")
	if !ok || got != conn {
		t.Fatal("decline must return the conn")
	}
	if !sc.IsMuted(conn) {
		t.Fatal("declined conn must be muted")
	}
	sc.Unmute(conn)
	if sc.IsMuted(conn) {
		t.Fatal("unmute must clear")
	}
}

func TestScreenDeadline(t *testing.T) {
	sc := NewPowScreener(nil)
	now := time.Now()
	overdue := &websocket.Conn{}
	future := &websocket.Conn{}
	sc.Issue(overdue, "10.0.0.1", 1, screenPreset, "s", "overdue", now.Add(-time.Second))
	sc.Issue(future, "10.0.0.2", 1, screenPreset, "s", "future", now.Add(time.Hour))
	// Normal mode: overdue mutes, future untouched.
	kick, mute := sc.CheckDeadline(now, false)
	if len(kick) != 0 || len(mute) != 1 || mute[0] != overdue {
		t.Fatalf("normal overdue must mute, got kick=%v mute=%v", kick, mute)
	}
	if !sc.IsMuted(overdue) {
		t.Fatal("muted conn must report muted")
	}
	if sc.IsMuted(future) {
		t.Fatal("future challenge must not mute yet")
	}
	// Under attack: overdue kicks.
	overdue2 := &websocket.Conn{}
	sc.Issue(overdue2, "10.0.0.3", 1, screenPreset, "s", "overdue2", now.Add(-time.Second))
	kick, mute = sc.CheckDeadline(now, true)
	if len(kick) != 1 || kick[0] != overdue2 || len(mute) != 0 {
		t.Fatalf("under attack overdue must kick, got kick=%v mute=%v", kick, mute)
	}
}

func TestScreenDropConn(t *testing.T) {
	sc := NewPowScreener(nil)
	conn := &websocket.Conn{}
	sc.Issue(conn, "10.0.0.1", 1, screenPreset, "s", "ch-x", time.Now().Add(time.Minute))
	sc.Defer("10.0.0.1", time.Now().Add(time.Hour))
	sc.DropConn(conn)
	if sc.IsMuted(conn) {
		t.Fatal("dropped conn must not be muted")
	}
	if _, ok := sc.Resolve("ch-x", 0); ok {
		t.Fatal("dropped challenge must be gone")
	}
}

func TestUnicastSkipsUnregistered(t *testing.T) {
	s := NewChatServer()
	conn := &websocket.Conn{}
	sess := &ClientSession{Conn: conn, Send: make(chan []byte, 1)}
	// Not in Hub.Clients: must drop silently, never send (a send here
	// would race unregisterClient's close(Send)).
	s.unicast(sess, "hello")
	select {
	case <-sess.Send:
		t.Fatal("must not send to an unregistered session")
	default:
	}
	// Registered: delivers.
	s.Hub.Clients[conn] = sess
	s.unicast(sess, "world")
	select {
	case got := <-sess.Send:
		if string(got) != "world" {
			t.Fatalf("got %q", got)
		}
	default:
		t.Fatal("registered session must receive")
	}
}
