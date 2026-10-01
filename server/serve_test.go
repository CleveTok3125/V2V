package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/wire"
)

// readLimitFor must never yield a negative limit (gorilla treats a
// negative limit as unlimited) even for an oversized configured value.
func TestReadLimitFor(t *testing.T) {
	if got := readLimitFor(1000); got != 3000 {
		t.Fatalf("readLimitFor(1000) = %d, want 3000", got)
	}
	if got := readLimitFor(0); got != 0 {
		t.Fatalf("readLimitFor(0) = %d, want 0", got)
	}
	if got := readLimitFor(-5); got != 0 {
		t.Fatalf("readLimitFor(-5) = %d, want 0", got)
	}
	if got := readLimitFor(math.MaxInt); got < 0 {
		t.Fatalf("readLimitFor(MaxInt) = %d, must not go negative", got)
	}
}

// serveAuthenticated must start WritePump before registerClient, then
// announce the history window and the join. The peer pages itself.
func TestServeAuthenticated_HistoryInfoThenJoin(t *testing.T) {
	old := Cfg.Dynamic.Load()
	Cfg.Dynamic.Store(config.DefaultDynamic())
	defer Cfg.Dynamic.Store(old)
	oldStatic := Cfg.Static
	Cfg.Static.Timezone = time.UTC
	defer func() { Cfg.Static = oldStatic }()

	s := NewChatServer()
	for i := 0; i < 600; i++ {
		s.Chain.appendMessageToHistory(tagLine(uint64(i+1), "chat", "", "line"))
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
		Send:        make(chan []byte, 2048),
		DisplayName: "Tester#abcd",
		Perms:       GetDefaultPermission(),
	}
	done := make(chan struct{})
	go func() {
		s.serveAuthenticated(session, "127.0.0.1")
		close(done)
	}()

	var sawInfo, sawJoin bool
	deadline := time.Now().Add(15 * time.Second)
	for {
		clientConn.SetReadDeadline(deadline)
		_, msg, err := clientConn.ReadMessage()
		if err != nil {
			t.Fatalf("read failed before history_info + join: %v", err)
		}
		var info HistoryInfo
		if json.Unmarshal(msg, &info) == nil && info.Type == "history_info" {
			sawInfo = true
		}
		if strings.Contains(string(msg), "đã tham gia phòng chat") {
			sawJoin = true
		}
		if sawInfo && sawJoin {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for history_info + join")
		}
	}

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
		if wire.HasTag(markerTags(string(msg)), wire.TagHistoryOlder) {
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
