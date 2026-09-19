package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/gorilla/websocket"
)

// With a history longer than the Send buffer (600 > 256), registering
// before WritePump blocks forever inside SendChatHistory because no reader
// is draining the channel yet. serveAuthenticated must start the pump first.
func TestServeAuthenticated_LargeHistoryNoDeadlock(t *testing.T) {
	old := Cfg.Dynamic.Load()
	Cfg.Dynamic.Store(config.DefaultDynamic())
	defer Cfg.Dynamic.Store(old)
	oldStatic := Cfg.Static
	Cfg.Static.Timezone = time.UTC
	defer func() { Cfg.Static = oldStatic }()

	s := NewChatServer()
	const historyLines = 600
	for i := 0; i < historyLines; i++ {
		s.Chain.appendMessageToHistory(fmt.Sprintf("legacy line %04d", i))
	}

	serverConnCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- conn
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	serverConn := <-serverConnCh

	session := &ClientSession{
		Conn: serverConn,
		// Oversized on purpose: replay sends are non-blocking, and a
		// full buffer here would drop lines and flake the count below.
		Send:        make(chan []byte, 2048),
		DisplayName: "Tester#abcd",
		Perms:       GetDefaultPermission(),
	}
	done := make(chan struct{})
	go func() {
		s.serveAuthenticated(session, "127.0.0.1")
		close(done)
	}()

	// Drain everything the server pushes: 500 capped history lines framed
	// by header/footer, then the date line and our own join broadcast.
	var historyCount int
	var inHistory, sawFooter, sawJoin bool
	deadline := time.Now().Add(15 * time.Second)
	for {
		clientConn.SetReadDeadline(deadline)
		_, msg, err := clientConn.ReadMessage()
		if err != nil {
			t.Fatalf("read failed before join seen: %v", err)
		}
		text := string(msg)
		switch {
		case strings.Contains(text, "Lịch sử chat gần đây"):
			inHistory = true
		case strings.Contains(text, "Kết thúc lịch sử"):
			inHistory, sawFooter = false, true
		case inHistory:
			historyCount++
		case strings.Contains(text, "đã tham gia phòng chat"):
			sawJoin = true
		}
		if sawFooter && sawJoin {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for history + join (pump-order deadlock?)")
		}
	}
	if historyCount != 500 {
		t.Fatalf("expected capped 500 history lines, got %d", historyCount)
	}

	// Closing our side ends ReadPump, which must return serveAuthenticated.
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("serveAuthenticated did not return after client close")
	}
}

// A history_request over a live connection returns the window below
// the cutoff in replay format, terminated by a history_sync trailer.
func TestReadPump_HistoryRequest(t *testing.T) {
	old := Cfg.Dynamic.Load()
	Cfg.Dynamic.Store(config.DefaultDynamic())
	defer Cfg.Dynamic.Store(old)
	oldStatic := Cfg.Static
	Cfg.Static.Timezone = time.UTC
	defer func() { Cfg.Static = oldStatic }()

	s := NewChatServer()
	for _, h := range []uint64{1, 2, 3} {
		s.Chain.linkAndStore(WireMessage{Type: "chat", Time: "12:00", DisplayName: "A#0000", Text: "line", TmpID: h}, "")
	}

	serverConnCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- conn
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	serverConn := <-serverConnCh

	session := &ClientSession{
		Conn:        serverConn,
		Send:        make(chan []byte, 256),
		DisplayName: "Tester#abcd",
		Perms:       GetDefaultPermission(),
	}
	go session.WritePump()
	go s.ReadPump(session, "127.0.0.1")

	req, _ := json.Marshal(HistoryRequest{Type: "history_request", Before: 3, Limit: 10})
	if err := clientConn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var trailer HistorySync
	var sawOldHeader bool
	for {
		clientConn.SetReadDeadline(deadline)
		_, msg, err := clientConn.ReadMessage()
		if err != nil {
			t.Fatalf("read failed before trailer: %v", err)
		}
		var hs HistorySync
		if err := json.Unmarshal(msg, &hs); err == nil && hs.Type == "history_sync" {
			trailer = hs
			break
		}
		if strings.Contains(string(msg), "Lịch sử cũ") {
			sawOldHeader = true
		}
	}
	if !sawOldHeader {
		t.Fatal("segment must open with the older-history header")
	}
	if trailer.MinHeight != 1 || trailer.MaxHeight != 2 || trailer.Sent != 2 || trailer.Total != 2 {
		t.Fatalf("segment trailer wrong: %+v", trailer)
	}
}
