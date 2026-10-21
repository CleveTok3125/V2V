package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/config"
	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/lib/wire"
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

// An exposed identity that joins again gets a unicast warning, so the
// client learns the badge is burned without waiting for a later message.
func TestServeAuthenticated_WarnsExposedIdentity(t *testing.T) {
	testCfg(t)
	seed := bytes.Repeat([]byte{0x51}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	s := NewChatServer()
	s.markTripExposed(pubHex)
	client, server := dialAuthPair(t, s)
	session := &ClientSession{
		Conn:        server,
		Send:        make(chan []byte, 256),
		DisplayName: "An#abcd",
		Perms:       GetDefaultPermission(),
		TripPub:     pubHex,
	}
	done := make(chan struct{})
	go func() {
		s.serveAuthenticated(session, "127.0.0.1")
		close(done)
	}()

	var warn WireMessage
	deadline := time.Now().Add(10 * time.Second)
	for {
		client.SetReadDeadline(deadline)
		_, msg, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read failed before the join warning: %v", err)
		}
		var wm WireMessage
		if json.Unmarshal(msg, &wm) != nil {
			continue
		}
		if wire.HasTag(wm.Tags, wire.TagTripReveal) {
			warn = wm
			break
		}
	}
	if !strings.Contains(warn.Text, "đã lộ seed") {
		t.Fatalf("join warning must mention the exposed seed: %+v", warn)
	}

	client.Close()
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
	var contents int
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
		if wire.HasTag(markerTags(string(msg)), wire.TagHistory) {
			t.Fatalf("replay must not emit history markers, got %q", msg)
		}
		for _, line := range replayFrameLines(msg) {
			var inner HistorySync
			if err := json.Unmarshal([]byte(line), &inner); err == nil && inner.Type == "history_sync" {
				trailer = inner
				break
			}
			contents++
		}
		if trailer.Type == "history_sync" {
			break
		}
	}
	if contents != 2 {
		t.Fatalf("segment must carry 2 content lines, got %d", contents)
	}
	if trailer.MinHeight != 1 || trailer.MaxHeight != 2 || trailer.Sent != 2 || trailer.Total != 2 {
		t.Fatalf("segment trailer wrong: %+v", trailer)
	}
}
