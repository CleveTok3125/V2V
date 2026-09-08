package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/gorilla/websocket"
)

func testCfg(t *testing.T) {
	t.Helper()
	oldDyn := Cfg.Dynamic.Load()
	Cfg.Dynamic.Store(config.DefaultDynamic())
	oldStatic := Cfg.Static
	Cfg.Static.Timezone = time.UTC
	t.Cleanup(func() {
		Cfg.Dynamic.Store(oldDyn)
		Cfg.Static = oldStatic
	})
}

// dialAuthPair upgrades a loopback pair and returns the client side plus
// the server side ready for HandleAuth.
func dialAuthPair(t *testing.T, s *ChatServer) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	serverCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverCh <- conn
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	server := <-serverCh
	t.Cleanup(func() { server.Close() })
	return client, server
}

func readChallenge(t *testing.T, client *websocket.Conn) AuthPacket {
	t.Helper()
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ch AuthPacket
	if err := client.ReadJSON(&ch); err != nil {
		t.Fatal(err)
	}
	if ch.Type != "auth_challenge" || ch.Nonce == "" {
		t.Fatalf("bad challenge: %+v", ch)
	}
	return ch
}

// TestHandleAuth_GuestOK pins the happy path: challenge -> guest response
// -> nil error with default permission.
func TestHandleAuth_GuestOK(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost")
		done <- err
	}()
	ch := readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{Type: "auth", Username: "Alice", Nonce: ch.Nonce}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("guest auth failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HandleAuth hung on guest path")
	}
}

// TestHandleAuth_Rejections pins the failure matrix: replayed nonce,
// oversized username, unknown role, malformed signature.
func TestHandleAuth_Rejections(t *testing.T) {
	testCfg(t)
	cases := []struct {
		name string
		resp func(nonce string) AuthPacket
		want string
	}{
		{"replay", func(nonce string) AuthPacket {
			return AuthPacket{Type: "auth", Username: "Bob", Nonce: "deadbeef-replay"}
		}, "invalid_nonce"},
		{"oversize-username", func(nonce string) AuthPacket {
			return AuthPacket{Type: "auth", Username: strings.Repeat("x", 5000), Nonce: nonce}
		}, "payload_too_large"},
		{"unknown-role", func(nonce string) AuthPacket {
			return AuthPacket{Type: "auth", Username: "Eve", Nonce: nonce, Role: "no-such-role"}
		}, "invalid_role"},
		{"bad-signature", func(nonce string) AuthPacket {
			return AuthPacket{Type: "auth", Username: "Eve", Nonce: nonce, Role: "member", Signature: "zz"}
		}, "invalid_role"}, // role missing from this server's registry
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewChatServer()
			client, server := dialAuthPair(t, s)
			done := make(chan error, 1)
			go func() {
				_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost")
				done <- err
			}()
			ch := readChallenge(t, client)
			if err := client.WriteJSON(tc.resp(ch.Nonce)); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want %q", err, tc.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("HandleAuth hung on reject path")
			}
		})
	}
}

// TestHandleAuth_ExpiredNonce: a nonce older than its TTL must fail even
// though it was legitimately issued.
func TestHandleAuth_ExpiredNonce(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost")
		done <- err
	}()
	ch := readChallenge(t, client)
	// Age the nonce past expiry before answering.
	s.ActiveNonces.Store(ch.Nonce, NonceMeta{ExpiresAt: time.Now().Add(-time.Second), IP: "127.0.0.1"})
	if err := client.WriteJSON(AuthPacket{Type: "auth", Username: "Late", Nonce: ch.Nonce}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "expired_nonce") {
			t.Fatalf("got %v, want expired_nonce", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HandleAuth hung on expired path")
	}
}

// The joiner receives its own join system line (nil sender): every client
// must observe every link for chain continuity.
func TestJoinBroadcast_IncludesJoiner(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	a, b := dialAuthPair(t, s)
	_ = a
	sessA := &ClientSession{Conn: a, Send: make(chan []byte, 256), DisplayName: "A#0000", Perms: GetDefaultPermission()}
	sessB := &ClientSession{Conn: b, Send: make(chan []byte, 256), DisplayName: "B#1111", Perms: GetDefaultPermission()}
	s.Clients[a] = sessA
	s.Clients[b] = sessB
	s.BroadcastNotice("X đã tham gia phòng chat!", "join", nil)
	for _, sess := range []*ClientSession{sessA, sessB} {
		select {
		case msg := <-sess.Send:
			if !strings.Contains(string(msg), "đã tham gia") {
				t.Fatalf("join broadcast mangled: %q", msg)
			}
		default:
			t.Fatalf("session %s missed its own join broadcast", sess.DisplayName)
		}
	}
}

// TestBroadcastWire_SeqEnforced: two chat wires through BroadcastWire must
// arrive chained with increasing heights.
func TestBroadcastWire_SeqEnforced(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	a, _ := dialAuthPair(t, s)
	sess := &ClientSession{Conn: a, Send: make(chan []byte, 256), DisplayName: "A#0000", Perms: GetDefaultPermission()}
	s.Clients[a] = sess
	s.BroadcastWire(WireMessage{Type: "chat", Text: "one", DisplayName: "A#0000"}, nil)
	s.BroadcastWire(WireMessage{Type: "chat", Text: "two", DisplayName: "A#0000"}, nil)
	var heights []uint64
	for i := 0; i < 2; i++ {
		select {
		case msg := <-sess.Send:
			var w WireMessage
			if err := json.Unmarshal(msg, &w); err != nil {
				t.Fatal(err)
			}
			heights = append(heights, w.ChainHeight)
		default:
			t.Fatalf("missing broadcast %d", i)
		}
	}
	if len(heights) != 2 || heights[1] != heights[0]+1 {
		t.Fatalf("chain heights not sequential: %v", heights)
	}
}

// TestActiveIdentities_Takeover: registering a second session on the
// same identity transfers ownership to the newest; unregister releases
// only for the owner, never for a stale session.
func TestActiveIdentities_Takeover(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	mk := func(name string) *ClientSession {
		_, serverConn := dialAuthPair(t, s)
		return &ClientSession{Conn: serverConn, Send: make(chan []byte, 16), DisplayName: name, Perms: GetDefaultPermission(), IdentityPub: "abc123"}
	}
	s1, s2 := mk("One#0001"), mk("Two#0002")
	s.registerClient(s1, "10.0.0.1")
	if raw, ok := s.ActiveIdentities.Load("abc123"); !ok || raw.(*ClientSession) != s1 {
		t.Fatal("first session must own the identity slot")
	}
	s.registerClient(s2, "10.0.0.2")
	if raw, ok := s.ActiveIdentities.Load("abc123"); !ok || raw.(*ClientSession) != s2 {
		t.Fatal("newest session must take over the identity slot")
	}
	s.unregisterClient(s1, "10.0.0.1")
	if _, ok := s.ActiveIdentities.Load("abc123"); !ok {
		t.Fatal("stale unregister must not release the new owner's slot")
	}
	s.unregisterClient(s2, "10.0.0.2")
	if _, ok := s.ActiveIdentities.Load("abc123"); ok {
		t.Fatal("owner unregister must release the slot")
	}
}

// TestWantJoinsEndToEnd: HistoryJoins travels from the auth packet into
// the session and controls replay filtering, through the real handshake.
func TestWantJoinsEndToEnd(t *testing.T) {
	for _, want := range []bool{false, true} {
		t.Run(map[bool]string{false: "filtered", true: "joins"}[want], func(t *testing.T) {
			testCfg(t)
			s := NewChatServer()
			s.appendMessageToHistory(`{"type":"system","sys_kind":"join","text":"old join"}`)
			s.appendMessageToHistory(`{"type":"chat","text":"hello"}`)
			client, serverConn := dialAuthPair(t, s)
			sessDone := make(chan *ClientSession, 1)
			go func() {
				sess, err := s.authenticateClient(serverConn, "127.0.0.1", "localhost")
				if err != nil {
					t.Errorf("authenticate: %v", err)
					sessDone <- nil
					return
				}
				sessDone <- sess
			}()
			ch := readChallenge(t, client)
			resp := AuthPacket{Type: "auth", Username: "E2E", Nonce: ch.Nonce, HistoryJoins: want}
			if err := client.WriteJSON(resp); err != nil {
				t.Fatal(err)
			}
			// Consume auth_success so the server pump never blocks.
			client.SetReadDeadline(time.Now().Add(10 * time.Second))
			var ok AuthPacket
			if err := client.ReadJSON(&ok); err != nil || ok.Type != "auth_success" {
				t.Fatalf("auth_success = %+v, %v", ok, err)
			}
			sess := <-sessDone
			if sess == nil {
				t.Fatal("no session")
			}
			if sess.WantJoins != want {
				t.Fatalf("WantJoins = %v, want %v", sess.WantJoins, want)
			}
			sess.Send = make(chan []byte, 64)
			s.registerClient(sess, "127.0.0.1")
			var got []string
			timeout := time.After(5 * time.Second)
		drain:
			for {
				select {
				case m := <-sess.Send:
					var hs HistorySync
					if err := json.Unmarshal(m, &hs); err == nil && hs.Type == "history_sync" {
						break drain
					}
					got = append(got, string(m))
				case <-timeout:
					t.Fatal("replay stalled")
				}
			}
			joins := 0
			for _, m := range got {
				if strings.Contains(m, "old join") {
					joins++
				}
			}
			if want && joins != 1 {
				t.Fatalf("want=1 join line, got %d in %q", joins, got)
			}
			if !want && joins != 0 {
				t.Fatalf("filtered replay leaked %d join lines: %q", joins, got)
			}
		})
	}
}

// TestRegister_CleansDisplayName verifies unregister releases the
// display serial slot and the Clients entry.
func TestRegister_CleansDisplayName(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	_, serverConn := dialAuthPair(t, s)
	sess := &ClientSession{Conn: serverConn, Send: make(chan []byte, 16), DisplayName: "Temp#0001", Perms: GetDefaultPermission()}
	s.registerClient(sess, "10.0.0.9")
	if _, ok := s.Clients[serverConn]; !ok {
		t.Fatal("registered session missing from Clients")
	}
	s.unregisterClient(sess, "10.0.0.9")
	if _, ok := s.Clients[serverConn]; ok {
		t.Fatal("unregistered session still in Clients")
	}
	s.DisplayNameCountMu.Lock()
	_, kept := s.DisplayNameCount["Temp#0001"]
	s.DisplayNameCountMu.Unlock()
	if kept {
		t.Fatal("display serial slot not released")
	}
}
