package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Behavior coverage for every slash command: each handler is driven
// through dispatch on a stubbed session and its visible effect is
// asserted (output text or session state).

func behaviorSession(t *testing.T) (*Session, *chatSpy) {
	t.Helper()
	sess := sessionForDispatch(t)
	sess.Display.ActiveTab = TabChat
	return sess, &chatSpy{sess: sess}
}

func TestCmdWhoamiLines(t *testing.T) {
	sess, out := behaviorSession(t)
	sess.Username = "Alice"
	sess.AuthType = "guest"
	if !sess.cmdWhoami("/whoami") {
		t.Fatal("/whoami must match")
	}
	if got := out.String(); !strings.Contains(got, "Alice") {
		t.Fatalf("whoami output missing username: %q", got)
	}
	if len(sess.Display.TabSys.lines) != 1 {
		t.Fatalf("guest whoami must emit 1 line, got %d", len(sess.Display.TabSys.lines))
	}

	sess, out = behaviorSession(t)
	sess.Username = "Bob"
	sess.AuthType = "key"
	sess.Role = "admin"
	sess.Unlimited = true
	sess.Prefix = "[A] "
	sess.cmdWhoami("/w")
	if got := out.String(); !strings.Contains(got, "Role:") {
		t.Fatalf("key whoami must show role: %q", got)
	}
	// Both rows ride one entry, so the block never gets spaced out.
	if len(sess.Display.TabSys.lines) != 1 {
		t.Fatalf("key whoami must emit 1 entry, got %d", len(sess.Display.TabSys.lines))
	}
	if got := out.String(); !strings.Contains(got, "Bob") || !strings.Contains(got, "\n  Role:") {
		t.Fatalf("key whoami rows must be adjacent: %q", got)
	}
}

func TestCmdStatusShowsConnection(t *testing.T) {
	sess, out := behaviorSession(t)
	sess.WSURL = "ws://example.com"
	sess.Connected = time.Now()
	sess.Display.ShowJoinLeave = true
	if !sess.cmdStatus("/status") {
		t.Fatal("/status must match")
	}
	if got := out.String(); !strings.Contains(got, "ws://example.com") {
		t.Fatalf("status must show server URL: %q", got)
	}
}

func TestCmdHelpListsCommands(t *testing.T) {
	sess, out := behaviorSession(t)
	if !sess.cmdHelp("/help") {
		t.Fatal("/help must match")
	}
	for _, want := range []string{"/reply", "/info", "/expand", "/copy", "/find"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help must list %s", want)
		}
	}
}

// TestCommandsAnswerInOneEntry: a command answers in one entry. Every entry
// carries a trailing gap row, so a command that emitted a row per entry would
// space its own output out.
func TestCommandsAnswerInOneEntry(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Session)
	}{
		{"help", func(s *Session) { s.cmdHelp("/h") }},
		{"notify listing", func(s *Session) { s.cmdNotify("/notify") }},
		{"whoami", func(s *Session) { s.cmdWhoami("/w") }},
		{"status", func(s *Session) { s.cmdStatus("/status") }},
		{"showjoin", func(s *Session) { s.cmdShowjoin("/sj") }},
		{"autoverify", func(s *Session) { s.cmdAutoverify("/av") }},
		{"meta", func(s *Session) { s.cmdMeta("/meta on") }},
		{"clearhistory", func(s *Session) { s.cmdClearhistory("/ch") }},
		{"copy empty", func(s *Session) { s.cmdCopy("/copy") }},
		{"older usage", func(s *Session) { s.cmdOlder("/older x") }},
		{"info usage", func(s *Session) { s.cmdInfo("/info") }},
		{"unknown command", func(s *Session) { s.dispatch("/halp") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sess, _ := behaviorSession(t)
			c.run(sess)
			if n := len(sess.Display.TabSys.lines); n != 1 {
				t.Fatalf("%s must answer in one entry, got %d", c.name, n)
			}
		})
	}
}

// TestCmdHelpRowsShareOneEntry: the help table is a header plus continuation
// rows, and the usage column is measured so it holds for every row except the
// one usage longer than the cap.
func TestCmdHelpRowsShareOneEntry(t *testing.T) {
	sess, out := behaviorSession(t)
	if !sess.cmdHelp("/h") {
		t.Fatal("/h must match")
	}
	if n := len(sess.Display.TabSys.lines); n != 1 {
		t.Fatalf("the table must be one entry, got %d", n)
	}
	got := out.String()
	if !strings.Contains(got, "Trợ giúp — danh sách lệnh:\n  - /help, /h") {
		t.Fatalf("the header must carry the first row: %q", got)
	}
	if strings.Contains(got, "\n\n  - ") {
		t.Fatalf("table rows must not be separated: %q", got)
	}
	col := -1
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "  - ") {
			continue
		}
		at := strings.Index(line, ": ")
		if at < 0 || at > len("  - ")+helpUsageCap {
			continue // the row that breaks out of the column
		}
		if col < 0 {
			col = at
			continue
		}
		if at != col {
			t.Fatalf("usage column %d, want %d: %q", at, col, line)
		}
	}
	if col < 0 {
		t.Fatal("no usage column found")
	}
}

// TestCmdNotifyListingIsOneAlignedEntry: the gate listing is one entry, with
// the states in one column whatever the tag's depth.
func TestCmdNotifyListingIsOneAlignedEntry(t *testing.T) {
	sess, out := behaviorSession(t)
	sess.Display.Notify = NotifyState{Muted: map[string]bool{}, PowMinTier: 1}
	if !sess.cmdNotify("/notify") {
		t.Fatal("/notify must match")
	}
	if n := len(sess.Display.TabSys.lines); n != 1 {
		t.Fatalf("the listing must be one entry, got %d", n)
	}
	got := out.String()
	if !strings.Contains(got, "Thông báo (tag → trạng thái):\n  system") {
		t.Fatalf("the header must carry the tree: %q", got)
	}
	col := -1
	for _, line := range strings.Split(got, "\n") {
		at := strings.Index(line, "BẬT")
		if at < 0 {
			at = strings.Index(line, "TẮT")
		}
		if at < 0 {
			continue
		}
		if col < 0 {
			col = at
			continue
		}
		if at != col {
			t.Fatalf("state column %d, want %d: %q", at, col, line)
		}
	}
	if col < 0 {
		t.Fatal("the listing must show gate states")
	}
}

// TestCmdInfoBlockIsOneEntry: the metadata detail is one block, so its rows
// never land a blank row apart.
func TestCmdInfoBlockIsOneEntry(t *testing.T) {
	sess, out := behaviorSession(t)
	seedIndexedWire(sess, 42)
	if !sess.cmdInfo("/info 42") {
		t.Fatal("/info must match")
	}
	if n := len(sess.Display.TabSys.lines); n != 1 {
		t.Fatalf("the metadata block must be one entry, got %d", n)
	}
	if got := out.String(); !strings.Contains(got, "[Local]: #42 — chi tiết metadata:") {
		t.Fatalf("the block must open with its header: %q", got)
	}
	if got := out.String(); strings.Contains(got, "\n\n  height:") {
		t.Fatalf("block rows must not be separated: %q", got)
	}
}

func TestCmdClearRepaintsGreeting(t *testing.T) {
	sess, _ := behaviorSession(t)
	sess.Username = "Tester"
	if !sess.cmdClear("/clear") {
		t.Fatal("/clear must match")
	}
	if got := entriesText(sess.Display.TabChat.lines); !strings.Contains(got, "Tester") {
		t.Fatalf("clear must greet username: %q", got)
	}
}

func TestCmdTabSwitches(t *testing.T) {
	sess, _ := behaviorSession(t)
	if !sess.cmdTab("/tab") {
		t.Fatal("/tab must match")
	}
	if sess.Display.ActiveTab != TabSystem {
		t.Fatalf("ActiveTab=%d, want TabSystem", sess.Display.ActiveTab)
	}
	sess.cmdTab("/tab 1")
	if sess.Display.ActiveTab != TabChat {
		t.Fatalf("ActiveTab=%d, want TabChat", sess.Display.ActiveTab)
	}
	sess.cmdTab("/t 2")
	if sess.Display.ActiveTab != TabSystem {
		t.Fatalf("ActiveTab=%d, want TabSystem", sess.Display.ActiveTab)
	}
	if !sess.cmdTab("/tab bogus") {
		t.Fatal("bogus tab arg must still match")
	}
	if sess.Display.ActiveTab != TabSystem {
		t.Fatalf("bogus tab arg must keep tab, got %d", sess.Display.ActiveTab)
	}
}

func TestCmdShowjoinToggles(t *testing.T) {
	sess, _ := behaviorSession(t)
	sess.Display.ShowJoinLeave = false
	sess.cmdShowjoin("/showjoin")
	if !sess.Display.ShowJoinLeave {
		t.Fatal("showjoin must turn on")
	}
	sess.cmdShowjoin("/sj")
	if sess.Display.ShowJoinLeave {
		t.Fatal("showjoin must turn off")
	}
}

func TestCmdAutoverifyToggles(t *testing.T) {
	sess, _ := behaviorSession(t)
	sess.Verify.AutoVerify = true
	sess.cmdAutoverify("/autoverify")
	if sess.Verify.AutoVerify {
		t.Fatal("autoverify must turn off")
	}
	sess.cmdAutoverify("/av")
	if !sess.Verify.AutoVerify {
		t.Fatal("autoverify must turn on")
	}
}

func TestCmdMetaSwitch(t *testing.T) {
	sess, out := behaviorSession(t)
	sess.Display.ShowMeta = true
	sess.cmdMeta("/meta off")
	if sess.Display.ShowMeta {
		t.Fatal("meta off must hide")
	}
	sess.cmdMeta("/meta on")
	if !sess.Display.ShowMeta {
		t.Fatal("meta on must show")
	}
	sess.cmdMeta("/meta")
	if sess.Display.ShowMeta {
		t.Fatal("bare meta must toggle")
	}
	sess.cmdMeta("/meta bogus")
	if sess.Display.ShowMeta {
		t.Fatal("bad meta arg must keep state")
	}
	if got := out.String(); !strings.Contains(got, "/meta") {
		t.Fatalf("bad meta arg must print usage: %q", got)
	}
}

func seedIndexedWire(sess *Session, height uint64) WireMessage {
	wire := WireMessage{
		Type: "chat", Time: "15:04", DisplayName: "Alice",
		Text: "hello world", TmpID: 7,
		ChainPrev:   "0000000000000000000000000000000000000000000000000000000000000000",
		ChainHash:   "abcd123400000000000000000000000000000000000000000000000000000000",
		ChainHeight: height,
		ChainVer:    2,
	}
	sess.Chain.WireIdx.put(wire)
	return wire
}

func TestCmdFindSearchesBuffer(t *testing.T) {
	sess, out := behaviorSession(t)
	seedChatHeight(sess, 1234)
	if !sess.cmdFind("/find 1234") {
		t.Fatal("/find must match")
	}
	// The hit is nested inside the local block, whole block and all.
	if got := out.String(); !strings.Contains(got, "Tìm #1234 trong bộ nhớ:") ||
		!strings.Contains(got, "#1234:abcd") || !strings.Contains(got, "hi") {
		t.Fatalf("find must nest the hit block: %q", got)
	}
	if got := out.String(); strings.Contains(got, "<<<<<<<") {
		t.Fatalf("find must not use the heredoc frame: %q", got)
	}

	sess, out = behaviorSession(t)
	seedChatHeight(sess, 1234)
	sess.cmdFind("/find 9999")
	if got := out.String(); !strings.Contains(got, "Không thấy") {
		t.Fatalf("missing height must report: %q", got)
	}
	if got := out.String(); strings.Contains(got, "<<<<<<<") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("missing height must not print a frame: %q", got)
	}

	sess, out = behaviorSession(t)
	if !sess.cmdFind("/find") {
		t.Fatal("bare /find must match")
	}
	if out.Len() == 0 {
		t.Fatal("bare /find must explain usage")
	}
}

func TestCmdInfoShowsMetadata(t *testing.T) {
	sess, out := behaviorSession(t)
	seedIndexedWire(sess, 42)
	if !sess.cmdInfo("/info 42") {
		t.Fatal("/info must match")
	}
	if got := out.String(); !strings.Contains(got, "chi tiết metadata") {
		t.Fatalf("info must show metadata: %q", got)
	}

	sess, out = behaviorSession(t)
	seedIndexedWire(sess, 42)
	sess.cmdInfo("/info 42:abcd")
	if got := out.String(); !strings.Contains(got, "chi tiết metadata") {
		t.Fatalf("matching hash suffix must show metadata: %q", got)
	}

	sess, out = behaviorSession(t)
	seedIndexedWire(sess, 42)
	sess.cmdInfo("/info 42:ffff")
	if got := out.String(); !strings.Contains(got, "hash khác") {
		t.Fatalf("wrong hash suffix must warn: %q", got)
	}

	sess, out = behaviorSession(t)
	sess.cmdInfo("/info 9999")
	if got := out.String(); !strings.Contains(got, "không còn trong bộ nhớ") {
		t.Fatalf("evicted height must report: %q", got)
	}

	sess, out = behaviorSession(t)
	if !sess.cmdInfo("/info") {
		t.Fatal("bare /info must match")
	}
	if out.Len() == 0 {
		t.Fatal("bare /info must explain usage")
	}
}

func TestCmdExpandReplaysFull(t *testing.T) {
	sess, out := behaviorSession(t)
	seedIndexedWire(sess, 42)
	sess.Display.TabChat.append(entry{body: "| Alice: hello wo" + expandTrailer(42), height: 42, collapsed: true})
	if !sess.cmdExpand("/expand 42") {
		t.Fatal("/expand must match")
	}
	got := out.String()
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("expand must not use the heredoc frame: %q", got)
	}
	if !strings.Contains(got, "Mở rộng #42:") || !strings.Contains(got, "hello world") {
		t.Fatalf("expand must nest the full block: %q", got)
	}

	sess, out = behaviorSession(t)
	sess.cmdExpand("/expand 9999")
	if got := out.String(); !strings.Contains(got, "trôi khỏi bộ nhớ") {
		t.Fatalf("evicted height must report: %q", got)
	}

	sess, out = behaviorSession(t)
	seedIndexedWire(sess, 42)
	sess.cmdExpand("/expand 42")
	if got := out.String(); !strings.Contains(got, "không thu gọn") {
		t.Fatalf("plain message must report not collapsed: %q", got)
	}

	sess, out = behaviorSession(t)
	if !sess.cmdExpand("/expand") {
		t.Fatal("bare /expand must match")
	}
	if out.Len() == 0 {
		t.Fatal("bare /expand must explain usage")
	}
}

func TestCmdCopyBranches(t *testing.T) {
	sess, out := behaviorSession(t)
	if !sess.cmdCopy("/copy") {
		t.Fatal("bare /copy must match")
	}
	if got := out.String(); !strings.Contains(got, "Chưa có tin nhắn nào") {
		t.Fatalf("bare /copy with no chat must report empty: %q", got)
	}

	sess, out = behaviorSession(t)
	sess.Display.ActiveTab = TabSystem
	sess.cmdCopy("/copy")
	if got := out.String(); !strings.Contains(got, "Tab 2 chưa có gì") {
		t.Fatalf("bare /copy with no system must report empty: %q", got)
	}

	sess, out = behaviorSession(t)
	seedIndexedWire(sess, 42)
	sess.Display.TabChat.append(chatBlockForTest(42, "abcd0000", "15:04", "Alice", "hi"))
	sess.cmdCopy("/copy")
	if got := out.String(); strings.Contains(got, "Dùng /copy") || strings.Contains(got, "Chưa có") {
		t.Fatalf("bare /copy with chat must copy latest, got: %q", got)
	}

	sess, out = behaviorSession(t)
	sess.Display.ActiveTab = TabSystem
	sess.Display.TabSys.append(entry{header: "system notice", kind: kindSystem})
	sess.cmdCopy("/copy")
	if got := out.String(); strings.Contains(got, "Dùng /copy") || strings.Contains(got, "Chưa có") {
		t.Fatalf("bare /copy with system must copy latest, got: %q", got)
	}

	sess, out = behaviorSession(t)
	sess.cmdCopy("/copy 9999")
	if got := out.String(); !strings.Contains(got, "không còn trong bộ nhớ") {
		t.Fatalf("evicted height must report: %q", got)
	}

	sess, out = behaviorSession(t)
	sess.cmdCopy("/copy abc")
	if got := out.String(); !strings.Contains(got, "Dùng /copy") {
		t.Fatalf("bad arg must print usage: %q", got)
	}
}

func TestCopyLatestSelectsNewest(t *testing.T) {
	sess, _ := behaviorSession(t)
	sess.Chain.WireIdx.put(WireMessage{Type: "chat", Text: "first", ChainHeight: 10, ChainHash: "aa"})
	sess.Chain.WireIdx.put(WireMessage{Type: "chat", Text: "second", ChainHeight: 20, ChainHash: "bb"})
	sess.Display.TabChat.append(chatBlockForTest(10, "aa", "15:04", "Alice", "FIRST-RENDERED"))
	sess.Display.TabChat.append(chatBlockForTest(20, "bb", "15:05", "Bob", "SECOND-RENDERED"))
	sess.Display.TabChat.append(noticeEntry("[Local]: xong"))
	sess.Display.TabChat.append(entry{header: "join", kind: kindSystem})
	sess.Display.DisplayMu.Lock()
	h, txt, ok := sess.latestChatCopy()
	sess.Display.DisplayMu.Unlock()
	if !ok || h != 20 || txt != "second" {
		t.Fatalf("latest chat = %d %q %v, want 20 second", h, txt, ok)
	}

	sess, _ = behaviorSession(t)
	sess.Display.TabChat.append(chatBlockForTest(30, "cc", "15:06", "Alice", "fallback-body"))
	sess.Display.DisplayMu.Lock()
	h, txt, ok = sess.latestChatCopy()
	sess.Display.DisplayMu.Unlock()
	if !ok || h != 30 || txt != "fallback-body" {
		t.Fatalf("evicted fallback = %d %q %v, want 30 fallback-body", h, txt, ok)
	}

	sess, _ = behaviorSession(t)
	sess.Display.ActiveTab = TabSystem
	sess.Display.TabSys.append(entry{header: "sys-one", kind: kindSystem})
	sess.Display.TabSys.append(noticeEntry("[Local]: xong"))
	sess.Display.TabSys.append(entry{header: "2026-10-17", kind: kindSystem, banner: true, day: "2026-10-17"})
	sess.Display.TabSys.append(entry{header: "sys-two", kind: kindSystem})
	sess.Display.DisplayMu.Lock()
	txt, ok = sess.latestSystemCopy()
	sess.Display.DisplayMu.Unlock()
	if !ok || txt != "sys-two" {
		t.Fatalf("latest system = %q %v, want sys-two", txt, ok)
	}

	// Tab 2 is the full local and system log: a local line is a copy
	// source just like a server notice, and a status line about an
	// earlier copy is not, so a repeated /copy repeats the content.
	sess, _ = behaviorSession(t)
	sess.Display.TabSys.append(entry{header: "sys-one", kind: kindSystem})
	sess.Display.TabSys.append(noticeEntry("[Local]: seed: abc\n  pub: def"))
	sess.Display.DisplayMu.Lock()
	txt, ok = sess.latestSystemCopy()
	sess.Display.DisplayMu.Unlock()
	if !ok || txt != "[Local]: seed: abc\n  pub: def" {
		t.Fatalf("local line must be copyable in tab 2: %q %v", txt, ok)
	}
	sess.Display.DisplayMu.Lock()
	sess.emitStatusLocked("[Local]: Đã copy dòng mới nhất tab 2.\n")
	txt, ok = sess.latestSystemCopy()
	sess.Display.DisplayMu.Unlock()
	if !ok || txt != "[Local]: seed: abc\n  pub: def" {
		t.Fatalf("status line must not become a copy source: %q %v", txt, ok)
	}

	sess, _ = behaviorSession(t)
	sess.Display.DisplayMu.Lock()
	_, _, ok = sess.latestChatCopy()
	sess.Display.DisplayMu.Unlock()
	if ok {
		t.Fatal("empty chat must report false")
	}
	sess.Display.ActiveTab = TabSystem
	sess.Display.DisplayMu.Lock()
	_, ok = sess.latestSystemCopy()
	sess.Display.DisplayMu.Unlock()
	if ok {
		t.Fatal("empty system must report false")
	}
}

func TestCmdClearhistoryRemovesFile(t *testing.T) {
	oldHistory := historyFile
	defer func() { historyFile = oldHistory }()
	historyFile = filepath.Join(t.TempDir(), "history.tmp")
	if err := os.WriteFile(historyFile, []byte("typed lines"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, out := behaviorSession(t)
	if !sess.cmdClearhistory("/clearhistory") {
		t.Fatal("/clearhistory must match")
	}
	if _, err := os.Stat(historyFile); !os.IsNotExist(err) {
		t.Fatal("history file must be removed")
	}
	if got := out.String(); !strings.Contains(got, "Đã xóa") {
		t.Fatalf("clearhistory must confirm: %q", got)
	}
}

func TestDispatchQuitExitsCleanly(t *testing.T) {
	sess, _ := behaviorSession(t)
	sess.Conn = &stubConn{}
	sess.Quitting = make(chan bool, 1)
	// Pump already exited: skip the 500ms gracefulQuit cap, this test
	// only asserts the quit action and the Quitting signal.
	close(sess.PumpDone)
	_, act := sess.dispatch("/quit")
	if act != cmdQuit {
		t.Fatalf("act=%v, want cmdQuit", act)
	}
	select {
	case <-sess.Quitting:
	default:
		t.Fatal("quit must signal the session")
	}
}
