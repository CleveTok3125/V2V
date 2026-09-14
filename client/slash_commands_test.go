package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Behavior coverage for every slash command: each handler is driven
// through dispatch on a stubbed session and its visible effect is
// asserted (output text or session state).

func behaviorSession(t *testing.T) (*Session, *bytes.Buffer) {
	t.Helper()
	sess := sessionForDispatch(t)
	var out bytes.Buffer
	sess.Out = &out
	sess.ActiveTab = TabChat
	return sess, &out
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
	if len(sess.TabSys.lines) != 1 {
		t.Fatalf("guest whoami must emit 1 line, got %d", len(sess.TabSys.lines))
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
	if len(sess.TabSys.lines) != 2 {
		t.Fatalf("key whoami must emit 2 lines, got %d", len(sess.TabSys.lines))
	}
}

func TestCmdStatusShowsConnection(t *testing.T) {
	sess, out := behaviorSession(t)
	sess.WSURL = "ws://example.com"
	sess.Connected = time.Now()
	sess.ShowJoinLeave = true
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

func TestCmdClearRepaintsGreeting(t *testing.T) {
	sess, out := behaviorSession(t)
	sess.Username = "Tester"
	if !sess.cmdClear("/clear") {
		t.Fatal("/clear must match")
	}
	got := out.String()
	if !strings.Contains(got, "\033[H\033[2J") {
		t.Fatalf("clear must wipe screen: %q", got)
	}
	if !strings.Contains(got, "Tester") {
		t.Fatalf("clear must greet username: %q", got)
	}
}

func TestCmdTabSwitches(t *testing.T) {
	sess, _ := behaviorSession(t)
	if !sess.cmdTab("/tab") {
		t.Fatal("/tab must match")
	}
	if sess.ActiveTab != TabSystem {
		t.Fatalf("ActiveTab=%d, want TabSystem", sess.ActiveTab)
	}
	sess.cmdTab("/tab 1")
	if sess.ActiveTab != TabChat {
		t.Fatalf("ActiveTab=%d, want TabChat", sess.ActiveTab)
	}
	sess.cmdTab("/t 2")
	if sess.ActiveTab != TabSystem {
		t.Fatalf("ActiveTab=%d, want TabSystem", sess.ActiveTab)
	}
	if !sess.cmdTab("/tab bogus") {
		t.Fatal("bogus tab arg must still match")
	}
	if sess.ActiveTab != TabSystem {
		t.Fatalf("bogus tab arg must keep tab, got %d", sess.ActiveTab)
	}
}

func TestCmdShowjoinToggles(t *testing.T) {
	sess, _ := behaviorSession(t)
	sess.ShowJoinLeave = false
	sess.cmdShowjoin("/showjoin")
	if !sess.ShowJoinLeave {
		t.Fatal("showjoin must turn on")
	}
	sess.cmdShowjoin("/sj")
	if sess.ShowJoinLeave {
		t.Fatal("showjoin must turn off")
	}
}

func TestCmdAutoverifyToggles(t *testing.T) {
	sess, _ := behaviorSession(t)
	sess.AutoVerify = true
	sess.cmdAutoverify("/autoverify")
	if sess.AutoVerify {
		t.Fatal("autoverify must turn off")
	}
	sess.cmdAutoverify("/av")
	if !sess.AutoVerify {
		t.Fatal("autoverify must turn on")
	}
}

func TestCmdMetaSwitch(t *testing.T) {
	sess, out := behaviorSession(t)
	sess.ShowMeta = true
	sess.cmdMeta("/meta off")
	if sess.ShowMeta {
		t.Fatal("meta off must hide")
	}
	sess.cmdMeta("/meta on")
	if !sess.ShowMeta {
		t.Fatal("meta on must show")
	}
	sess.cmdMeta("/meta")
	if sess.ShowMeta {
		t.Fatal("bare meta must toggle")
	}
	out.Reset()
	sess.cmdMeta("/meta bogus")
	if sess.ShowMeta {
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
	sess.WireIdx.put(wire)
	return wire
}

func TestCmdFindSearchesBuffer(t *testing.T) {
	sess, out := behaviorSession(t)
	seedChatHeight(sess, 1234)
	if !sess.cmdFind("/find 1234") {
		t.Fatal("/find must match")
	}
	if got := out.String(); !strings.Contains(got, "Tìm #1234") {
		t.Fatalf("find must show header: %q", got)
	}

	sess, out = behaviorSession(t)
	seedChatHeight(sess, 1234)
	sess.cmdFind("/find 9999")
	if got := out.String(); !strings.Contains(got, "Không thấy") {
		t.Fatalf("missing height must report: %q", got)
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
	sess.TabChat.append("| Alice: hello wo\x1b[90m...\x1b[0m [Xem thêm: /expand #42]\n")
	if !sess.cmdExpand("/expand 42") {
		t.Fatal("/expand must match")
	}
	got := out.String()
	if !strings.Contains(got, "<<<<<<< #42") || !strings.Contains(got, ">>>>>>> #42") {
		t.Fatalf("expand must replay inside frame: %q", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Fatalf("expand must show full text: %q", got)
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
	if got := out.String(); !strings.Contains(got, "/copy") {
		t.Fatalf("bare /copy must print usage: %q", got)
	}

	sess, out = behaviorSession(t)
	sess.cmdCopy("/copy 9999")
	if got := out.String(); !strings.Contains(got, "không còn trong bộ nhớ") {
		t.Fatalf("evicted height must report: %q", got)
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
	sess.VerifyCh = make(chan verifyJob, 1)
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
