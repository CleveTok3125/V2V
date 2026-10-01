package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/wire"
)

// TestBroadcastDateCarriesMachineDay: the banner a client draws is a
// rendered string, so the day must also travel as data (sys_date) or the
// client can only recognise a repeat by comparing the text it already
// drew. The display order stays 02/01/2006 while the wire value is the
// machine format.
func TestBroadcastDateCarriesMachineDay(t *testing.T) {
	defer testChainCfg()()
	Cfg.Static.Timezone = time.UTC
	s := NewChatServer()
	now := time.Date(2026, 9, 27, 3, 27, 0, 0, time.UTC)

	s.Hub.LastMessageDateMu.Lock()
	s.Hub.LastMessageDate = ""
	s.Hub.LastMessageDateMu.Unlock()
	s.Hub.CheckAndBroadcastDate(now)

	if len(s.Chain.History) != 1 {
		t.Fatalf("one banner expected, got %d lines", len(s.Chain.History))
	}
	var banner WireMessage
	if err := json.Unmarshal([]byte(s.Chain.History[0]), &banner); err != nil {
		t.Fatal(err)
	}
	if banner.SysDate != "2026-09-27" {
		t.Fatalf("sys_date = %q, want 2026-09-27", banner.SysDate)
	}
	if !wire.HasTag(banner.Tags, wire.TagDate) {
		t.Fatalf("banner must be tagged as a date, got %v", banner.Tags)
	}
	if !strings.Contains(banner.Text, "27/09/2026") {
		t.Fatalf("banner text must keep the display order, got %q", banner.Text)
	}

	// Same day again: no second banner, the server tracks it.
	s.Hub.CheckAndBroadcastDate(now.Add(time.Hour))
	if len(s.Chain.History) != 1 {
		t.Fatalf("same day must not re-announce, got %d lines", len(s.Chain.History))
	}
}

// TestReplayFilterUsesTags: join/leave notices are dropped from a replay
// unless the session asked for them, and the decision is made on tags
// rather than on the banner wording.
func TestReplayFilterUsesTags(t *testing.T) {
	defer testChainCfg()()
	s := NewChatServer()
	s.Chain.History = append(s.Chain.History,
		noticeLine("join", "A joined"),
		noticeLine("leave", "A left"),
		noticeLine("date", "day marker"),
		tagLine(1, "chat", "", "hello"),
	)

	replayed := func(wantJoins bool) []WireMessage {
		session := &ClientSession{WantJoins: wantJoins, Send: make(chan []byte, 64)}
		s.Chain.sendReplayPage(session, s.Chain.History, "", replayJoin, pageMeta{})
		close(session.Send)
		var got []WireMessage
		for payload := range session.Send {
			for _, line := range strings.Split(strings.TrimRight(string(payload), "\n"), "\n") {
				if line == "" {
					continue
				}
				var stored WireMessage
				if err := json.Unmarshal([]byte(line), &stored); err != nil {
					continue
				}
				got = append(got, stored)
			}
		}
		return got
	}

	for _, stored := range replayed(false) {
		if wire.HasAnyTag(stored.Tags, wire.TagJoin, wire.TagLeave) {
			t.Errorf("join/leave must be filtered without WantJoins, got %q", stored.Text)
		}
	}

	var sawChat, sawDate, sawJoin bool
	for _, stored := range replayed(true) {
		if stored.Type == "chat" {
			sawChat = true
		}
		if wire.HasTag(stored.Tags, wire.TagDate) {
			sawDate = true
		}
		if wire.HasTag(stored.Tags, wire.TagJoin) {
			sawJoin = true
		}
	}
	if !sawChat || !sawDate {
		t.Errorf("replay must keep chat and date lines, chat=%v date=%v", sawChat, sawDate)
	}
	if !sawJoin {
		t.Error("replay must keep join notices when WantJoins is set")
	}
}

// TestBroadcastNoticeCompletesTheChain: producers name a leaf and the hub
// owns the chain. A partial chain would let a notice slip past a mute on
// its parent, so the expansion is pinned here rather than left to every
// caller to remember.
func TestBroadcastNoticeCompletesTheChain(t *testing.T) {
	defer testChainCfg()()
	Cfg.Static.Timezone = time.UTC
	s := NewChatServer()

	s.Hub.BroadcastNotice("A joined", []string{wire.TagJoin}, nil)
	s.Hub.BroadcastDate("--- Ngày ---", "2026-01-02", nil)

	var join, date WireMessage
	if err := json.Unmarshal([]byte(s.Chain.History[0]), &join); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(s.Chain.History[1]), &date); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{wire.TagRoot, wire.TagJoin} {
		if !wire.HasTag(join.Tags, want) {
			t.Errorf("join notice must carry %q, got %v", want, join.Tags)
		}
	}
	for _, want := range []string{wire.TagRoot, wire.TagDate} {
		if !wire.HasTag(date.Tags, want) {
			t.Errorf("date banner must carry %q, got %v", want, date.Tags)
		}
	}
	if date.SysDate != "2026-01-02" {
		t.Errorf("sys_date = %q, want 2026-01-02", date.SysDate)
	}
	// A notice that announces no day must not invent one.
	if join.SysDate != "" {
		t.Errorf("join notice must have no sys_date, got %q", join.SysDate)
	}
}

// TestServerNoticesCarryTags: every unicast notice travels as a tagged
// wire. A notice sent as a bare string leaves the client nothing to filter
// on, which is what forced it to match the wording — so this checks the
// framing, not just the tag.
func TestServerNoticesCarryTags(t *testing.T) {
	defer testChainCfg()()
	Cfg.Static.Timezone = time.UTC
	s := NewChatServer()
	conn := &websocket.Conn{}
	sess := &ClientSession{Conn: conn, IP: "192.0.2.1", Send: make(chan []byte, 8)}
	s.Hub.Clients[conn] = sess

	cases := []struct {
		name string
		text string
		leaf string
	}{
		{"limit", "[Hệ thống]: Bạn đang chat quá nhanh! Vui lòng đợi 10s.", wire.TagLimit},
		{"pow gate", "[Hệ thống]: Chat của bạn tạm dừng chờ xác minh.", wire.TagPowGate},
		{"screening", "[Hệ thống]: Xác minh PoW xong, chat mở lại bình thường.", wire.TagPowScreen},
		{"envelope", "[Hệ thống]: Tin nhắn thiếu ID phiên (tmp_id).", wire.TagEnvelope},
		{"trip", "[Hệ thống]: Chuỗi trip bị đứt (prev không khớp).", wire.TagTrip},
		{"filter", "[Hệ thống]: Tin nhắn chứa ký tự không hợp lệ.", wire.TagFilter},
		{"auth", "[He thong]: Danh tinh cua ban vua duoc dang nhap tu 192.0.2.9.", wire.TagAuth},
	}
	for _, tc := range cases {
		s.unicastNotice(sess, tc.text, tc.leaf)
		select {
		case payload := <-sess.Send:
			var notice WireMessage
			if err := json.Unmarshal(payload, &notice); err != nil {
				t.Errorf("%s: notice must be a wire, got %q: %v", tc.name, payload, err)
				continue
			}
			if notice.Type != "system" {
				t.Errorf("%s: type = %q, want system", tc.name, notice.Type)
			}
			if notice.Text != tc.text {
				t.Errorf("%s: text = %q, want %q", tc.name, notice.Text, tc.text)
			}
			if !wire.HasTag(notice.Tags, tc.leaf) {
				t.Errorf("%s: must carry %q, got %v", tc.name, tc.leaf, notice.Tags)
			}
			if !wire.HasTag(notice.Tags, wire.TagRoot) {
				t.Errorf("%s: must carry the root, got %v", tc.name, notice.Tags)
			}
			if notice.Seq != 0 {
				t.Errorf("%s: a unicast notice is not sequenced, got seq %d", tc.name, notice.Seq)
			}
			if len(s.Chain.History) != 0 {
				t.Errorf("%s: a unicast notice must not enter history", tc.name)
			}
		default:
			t.Errorf("%s: nothing delivered", tc.name)
		}
	}
}

// TestTripcodeTooLongRejectsWithAuthPacket: a client reads exactly one frame
// to decide whether it holds a session, and it branches on the type. A
// notice frame here parses cleanly, matches neither auth_success nor
// auth_failed, and the client walks into a UI with a dead socket — so the
// rejection has to arrive as an auth packet.
func TestTripcodeTooLongRejectsWithAuthPacket(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, err := s.authenticateClient(server, "127.0.0.1", "localhost", false)
		done <- err
	}()
	ch := readChallenge(t, client)
	// The client's own caps only cover the interactive prompt, so a
	// tripcode can reach the server over the configured limit.
	tooLong := strings.Repeat("k", Cfg.Dynamic.Load().MaxTripcodeLength+1)
	if err := client.WriteJSON(AuthPacket{Type: "auth", Username: "Alice", Nonce: ch.Nonce, Tripcode: tooLong}); err != nil {
		t.Fatal(err)
	}
	var answer AuthPacket
	if err := client.ReadJSON(&answer); err != nil {
		t.Fatal(err)
	}
	if answer.Type != "auth_failed" {
		t.Fatalf("pre-auth rejection must be an auth packet, got type %q", answer.Type)
	}
	if !strings.Contains(answer.Error, "Tripcode") {
		t.Fatalf("rejection must name the reason, got %q", answer.Error)
	}
	if err := <-done; !errors.Is(err, ErrTripcodeTooLong) {
		t.Fatalf("HandleAuth err = %v, want ErrTripcodeTooLong", err)
	}
}

// TestNoticeWireWithoutConfiguredTimezone: a notice stamps itself in the
// configured timezone, and Time.In panics on a nil location. Config always
// resolves one in production, so this only bites a caller that builds a
// server without it — but a panic there takes down whatever goroutine
// happened to send the notice, so the fallback is pinned.
func TestNoticeWireWithoutConfiguredTimezone(t *testing.T) {
	old := Cfg.Static.Timezone
	Cfg.Static.Timezone = nil
	t.Cleanup(func() { Cfg.Static.Timezone = old })

	got := noticeWire(time.Now(), "[Hệ thống]: x", wire.TagLimit)
	if got.Type != "system" {
		t.Fatalf("type = %q, want system", got.Type)
	}
	if !wire.HasTag(got.Tags, wire.TagLimit) {
		t.Fatalf("tags = %v, want the limit leaf", got.Tags)
	}
	if _, err := time.Parse("15:04", got.Time); err != nil {
		t.Fatalf("time %q must be a clock time: %v", got.Time, err)
	}
}
