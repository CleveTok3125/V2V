package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Session input-loop dispatch (moved from main).

// cmdAction tells the input loop what to do after a line.
type cmdAction int

const (
	cmdPass cmdAction = iota // not a command: fall through to the send path
	cmdDone                  // handled: read the next line
	cmdQuit                  // quit: break the input loop
)

// dispatch routes one input line. Table order matches the on-screen help.
func (s *Session) dispatch(text string) (string, cmdAction) {
	if text == "/quit" || text == "/q" {
		s.gracefulQuit()
		return text, cmdQuit
	}
	for _, cmd := range []func(string) bool{
		s.cmdWhoami, s.cmdStatus, s.cmdHelp, s.cmdShowjoin,
		s.cmdAutoverify, s.cmdTab, s.cmdClear,
	} {
		if cmd(text) {
			return text, cmdDone
		}
	}
	if next, act, ok := s.cmdReply(text); ok {
		if act != cmdPass {
			return next, act
		}
		text = next
	}
	for _, cmd := range []func(string) bool{
		s.cmdMeta, s.cmdFind, s.cmdExpand, s.cmdInfo,
		s.cmdCopy, s.cmdClearhistory, s.cmdOlder,
	} {
		if cmd(text) {
			// Inline /reply body intercepted by a command is never
			// sent: drop its one-shot target so the next plain
			// message does not inherit the quote.
			s.Pending.PendingReplyTo = 0
			return text, cmdDone
		}
	}
	// Unknown slash input: every built-in command was already matched
	// above, so anything still starting with "/" is a mistyped command
	// rejected locally and never broadcast. Known commands above
	// already returned.
	if isUnknownSlashCommand(text) {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Lệnh không tồn tại: %s. Gõ /help để xem danh sách.\n", text))
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		// Rejected input never sends: drop the one-shot reply target
		// so the next plain message does not inherit the quote.
		s.Pending.PendingReplyTo = 0
		return text, cmdDone
	}
	return text, cmdPass
}

// handleReadErr maps a ReadLine failure to a loop action: cancel
// resets draft state, any other error quits cleanly.
func (s *Session) handleReadErr(err error) cmdAction {
	if !errors.Is(err, ErrInputCancel) {
		s.gracefulQuit()
		return cmdQuit
	}
	// Reply targets attach to the next send only; reset first so a
	// rejected message never leaks its quote into a later one. The
	// draft/inline /reply handlers below re-arm it when due.
	s.Pending.PendingReplyTo = 0
	if s.Pending.ReplyDraft > 0 {
		s.Pending.ReplyDraft = 0
		s.Display.Term.SetPrompt("| > ")
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("| [Local]: Đã hủy reply nháp.\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return cmdDone
	}
	s.Display.Term.SetPrompt("| > ")
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback("| [Local]: Ctrl+C chỉ hủy dòng nhập, thoát app bằng Ctrl+D.\n")
	s.Display.DisplayMu.Unlock()
	s.Display.Term.Refresh()
	return cmdDone
}

// handleDraftGate routes a line through a pending reply draft:
// slash input aborts it, anything else becomes the quoted body.
func (s *Session) handleDraftGate(text string) {
	if strings.HasPrefix(text, "/") {
		// Any slash command aborts the draft, then runs normally
		// through the dispatch below (a "/" body could never send
		// anyway: the unknown-slash guard rejects it).
		s.Pending.ReplyDraft = 0
		s.Display.Term.SetPrompt("| > ")
	} else {
		// Draft body (codeblock fences included): attach and send.
		s.Pending.PendingReplyTo = s.Pending.ReplyDraft
		s.Pending.ReplyDraft = 0
		s.Display.Term.SetPrompt("| > ")
	}
}

// gracefulQuit closes the connection cleanly like /quit does, so both
// an explicit quit command and an EOF (Ctrl+D) leave no dangling state.
func (s *Session) gracefulQuit() {
	s.Quitting <- true
	s.flushChainTip()
	s.Verify.VerifyCloseOnce.Do(func() { close(s.Verify.VerifyCh) })
	s.Conn.WriteMessage(wsCloseMessage, []byte{})
	if s.TripPriv != nil {
		for i := range s.TripPriv {
			s.TripPriv[i] = 0
		}
	}
	fmt.Fprintf(s.Display.Out, "👋 Đang ngắt kết nối... Tạm biệt!\n")
	// Let the goodbye flush and the pump tear down instead of racing
	// them: return as soon as the pump exits, same 500ms cap as the
	// old fixed sleep when it never does.
	if s.PumpDone != nil {
		select {
		case <-s.PumpDone:
		case <-time.After(500 * time.Millisecond):
		}
	}
	notifyQuit()
}

func (s *Session) cmdWhoami(text string) bool {
	if !(text == "/whoami" || text == "/w") {
		return false
	}
	emitWhoami(&s.Display.DisplayMu, s.emitLocalFeedback, s.Username, s.AuthType, s.Role, s.Unlimited, s.Prefix)
	return true
}

func (s *Session) cmdStatus(text string) bool {
	if !(text == "/status") {
		return false
	}
	s.Display.ShowJoinMu.RLock()
	sj := "TẮT"
	if s.Display.ShowJoinLeave {
		sj = "BẬT"
	}
	s.Display.ShowJoinMu.RUnlock()
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback(fmt.Sprintf("| [Local]: Server: %s | Đã kết nối: %s | Phiên bản: %s | Show-join: %s\n",
		s.WSURL, time.Since(s.Connected).Round(time.Second), Version, sj))
	s.Display.DisplayMu.Unlock()
	return true
}

func (s *Session) cmdHelp(text string) bool {
	if !(text == "/help" || text == "/h") {
		return false
	}
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback("  [Trợ giúp]: Danh sách các lệnh có thể sử dụng:\n")
	s.emitLocalFeedback("    - /help, /h      : Hiển thị bảng trợ giúp này\n")
	s.emitLocalFeedback("    - /clear, /c     : Xóa sạch màn hình chat\n")
	s.emitLocalFeedback("    - /clearhistory, /ch: Xóa file lịch sử gõ phím lưu trên máy\n")
	s.emitLocalFeedback("    - /quit, /q      : Rời phòng chat và tắt ứng dụng\n")
	s.emitLocalFeedback("    - /showjoin, /sj : Bật/tắt hiện thông báo người khác ra vào phòng cho các tin kế tiếp\n")
	s.emitLocalFeedback("    - /whoami, /w    : Thông tin danh tính và quyền hiện tại\n")
	s.emitLocalFeedback("    - /status        : Trạng thái kết nối và phiên bản client\n")
	s.emitLocalFeedback("    - /autoverify, /av: Bật/tắt auto-verify trip (mặc định BẬT, queue FIFO, verify song song)\n")
	s.emitLocalFeedback("    - /info <n>[:hash]: Xem đầy đủ metadata tin nhắn (verify lại tại local)\n")
	s.emitLocalFeedback("    - /expand <n>, /xpan  : Mở đầy đủ tin bị thu gọn (vd /expand 1234)\n")
	s.emitLocalFeedback("    - /copy <n>[:hash]: Copy nội dung thô tin nhắn vào clipboard\n")
	s.emitLocalFeedback("    - /tab, /t [1|2]  : Chuyển tab chat / local & system\n")
	s.emitLocalFeedback("    - /meta, /m [on|off]: Hiện/ẩn dòng meta #height:hash (mặc định hiện, chain vẫn verify)\n")
	s.emitLocalFeedback("    - /find, /f <n>[:hash]: Tìm tin theo số height trong bộ nhớ (vd /find 1234)\n")
	s.emitLocalFeedback("    - /older [n]   : Lấy thêm n tin cũ hơn từ server (mặc định 50)\n")
	s.emitLocalFeedback("    - /reply <n>[:hash] text: Trả lời tin #n kèm quote (vd /reply 1234 đồng ý)\n")
	s.emitLocalFeedback("    - /reply <n>          : Soạn reply nháp, dòng tiếp theo là nội dung\n")
	s.emitLocalFeedback("    - Gõ @#n (vd @#1234) trong tin để nhắc tới tin khác (sáng lên khi còn trong bộ nhớ)\n")
	s.emitLocalFeedback("    - Lệnh lạ bắt đầu bằng / bị chặn, không gửi đi (muốn gửi chữ / đầu dòng thì dùng codeblock)\n")
	s.emitLocalFeedback("    - Gõ ``` ở đầu và cuối tin nhắn để gửi Code block / nhiều dòng (^C hủy nhập)\n")
	s.emitLocalFeedback("    - Bọc chữ trong `dấu backtick` để hiện nền riêng (inline code một dòng)\n")
	s.Display.DisplayMu.Unlock()
	return true
}

func (s *Session) cmdShowjoin(text string) bool {
	if !(text == "/showjoin" || text == "/sj") {
		return false
	}
	s.Display.ShowJoinMu.Lock()
	s.Display.ShowJoinLeave = !s.Display.ShowJoinLeave
	status := "ĐÃ TẮT"
	if s.Display.ShowJoinLeave {
		status = "ĐÃ BẬT"
	}
	s.Display.ShowJoinMu.Unlock()
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback(fmt.Sprintf("| [Local]: %s hiển thị thông báo người dùng ra/vào phòng cho các tin kế tiếp.\n", status))
	s.Display.DisplayMu.Unlock()
	return true
}

func (s *Session) cmdAutoverify(text string) bool {
	if !(text == "/autoverify" || text == "/av") {
		return false
	}
	s.Verify.AutoVerifyMu.Lock()
	s.Verify.AutoVerify = !s.Verify.AutoVerify
	status := "BẬT"
	if !s.Verify.AutoVerify {
		status = "TẮT"
	}
	s.Verify.AutoVerifyMu.Unlock()
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback(fmt.Sprintf("| [Local]: Auto-verify đã %s (mặc định BẬT, verify song song qua channel FIFO).\n", status))
	s.Display.DisplayMu.Unlock()
	return true
}

func (s *Session) cmdTab(text string) bool {
	if !(text == "/tab" || text == "/t" || strings.HasPrefix(text, "/tab ") || strings.HasPrefix(text, "/t ")) {
		return false
	}
	n := s.Display.ActiveTab
	if text == "/tab" || text == "/t" {
		if s.Display.ActiveTab == TabChat {
			n = TabSystem
		} else {
			n = TabChat
		}
	} else {
		rest := ""
		if strings.HasPrefix(text, "/tab ") {
			rest = strings.TrimSpace(strings.TrimPrefix(text, "/tab"))
		} else {
			rest = strings.TrimSpace(strings.TrimPrefix(text, "/t"))
		}
		if rest == "1" {
			n = TabChat
		} else if rest == "2" {
			n = TabSystem
		}
	}
	s.switchTab(n)
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback(tabBarLine(s.Display.ActiveTab))
	s.Display.PrintGen++
	s.Display.DisplayMu.Unlock()
	return true
}

func (s *Session) cmdClear(text string) bool {
	if !(text == "/clear" || text == "/c") {
		return false
	}
	fmt.Fprint(s.Display.Out, "\033[H\033[2J")
	greeting(s.Display.Out, s.Username)
	return true
}

func (s *Session) cmdReply(text string) (string, cmdAction, bool) {
	if !(text == "/reply" || strings.HasPrefix(text, "/reply ")) {
		return text, cmdPass, false
	}
	if !ClientCfg.ReplyEnabled() {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("| [Local]: Reply đã tắt trong config (ui.reply.enabled).\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return text, cmdDone, true
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/reply"))
	fields := strings.Fields(rest)
	var target, body string
	if len(fields) > 0 {
		target = fields[0]
		body = strings.TrimSpace(rest[len(target):])
	}
	height, suffix, err := parseFindArg(target)
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("| [Local]: Dùng /reply <height>[:hash] [tin nhắn] (vd /reply 1234 đồng ý; /reply 1234 để soạn nháp).\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return text, cmdDone, true
	}
	s.Display.DisplayMu.Lock()
	found := len(findMetaMatches(s.Display.TabChat.lines, height, suffix)) > 0 ||
		len(findMetaMatches(s.Display.TabSys.lines, height, suffix)) > 0
	s.Display.DisplayMu.Unlock()
	if !found {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ, không reply được.\n", height))
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return text, cmdDone, true
	}
	if body == "" {
		// Draft mode: quote now, body on the next line. Any slash
		// command or empty-line ^C aborts it (see loop top).
		s.Pending.ReplyDraft = height
		s.Display.Term.SetPrompt(fmt.Sprintf("| ↩ #%d > ", height))
		s.Display.DisplayMu.Lock()
		for _, q := range s.quoteLinesFor(height, false) {
			fmt.Fprint(s.Display.Out, q+"\n")
			s.Display.PrintGen++
		}
		s.emitLocalFeedback("| [Local]: Gõ nội dung reply (Enter gửi, ^C ở dòng trống hủy, lệnh / khác hủy draft).\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return text, cmdDone, true
	}
	// Quote validated: the body flows through the remaining dispatch
	// (later slash commands, unknown-slash guard, then send) with the
	// target attached one-shot.
	s.Pending.PendingReplyTo = height
	return body, cmdPass, true
}

func (s *Session) cmdMeta(text string) bool {
	if !(text == "/meta" || text == "/m" || strings.HasPrefix(text, "/meta ") || strings.HasPrefix(text, "/m ")) {
		return false
	}
	rest := ""
	if strings.HasPrefix(text, "/meta") {
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/meta"))
	} else {
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/m"))
	}
	s.Display.ShowMetaMu.Lock()
	switch rest {
	case "on":
		s.Display.ShowMeta = true
	case "off":
		s.Display.ShowMeta = false
	case "":
		s.Display.ShowMeta = !s.Display.ShowMeta
	default:
		s.Display.ShowMetaMu.Unlock()
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("| [Local]: Dùng /meta, /meta on hoặc /meta off.\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	state := "HIỆN"
	if !s.Display.ShowMeta {
		state = "ẨN"
	}
	s.Display.ShowMetaMu.Unlock()
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback(fmt.Sprintf("| [Local]: Dòng meta (#height:hash) %s (chain vẫn verify ngầm).\n", state))
	s.Display.DisplayMu.Unlock()
	s.Display.Term.Refresh()
	return true
}

func (s *Session) cmdFind(text string) bool {
	if !(text == "/find" || text == "/f" || strings.HasPrefix(text, "/find ") || strings.HasPrefix(text, "/f ")) {
		return false
	}
	rest := ""
	if strings.HasPrefix(text, "/find") {
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/find"))
	} else {
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/f"))
	}
	height, suffix, err := parseFindArg(rest)
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: %v.\n", err))
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	s.Display.DisplayMu.Lock()
	shown := 0
	header := fmt.Sprintf("| [Local]: Tìm #%d", height)
	if suffix != "" {
		header += ":" + suffix
	}
	s.emitLocalFeedback(header + " trong bộ nhớ:\n")
	// Snapshot matches before emitting: emitting appends to
	// sess.Display.TabSys, whose eviction could shift indices mid-scan.
	var hits []string
	for _, buf := range []*tabBuffer{s.Display.TabChat, s.Display.TabSys} {
		matches := findMetaMatches(buf.lines, height, suffix)
		for _, idx := range matches {
			if idx > 0 {
				hits = append(hits, buf.lines[idx-1])
			}
			hits = append(hits, buf.lines[idx])
		}
		shown += len(matches)
	}
	// Replay inside the same dim heredoc frame as /expand so replayed
	// lines never read as live chat. No buffer surgery: markers only.
	if shown > 0 {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: \x1b[90m<<<<<<< #%d\x1b[0m\n", height))
	}
	for _, h := range hits {
		s.emitLocalFeedback(h)
	}
	if shown == 0 {
		s.emitLocalFeedback("| [Local]: Không thấy (tin cũ đã bị evict khỏi bộ nhớ hoặc chưa sync).\n")
	} else {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: \x1b[90m>>>>>>> #%d\x1b[0m\n", height))
	}
	s.Display.DisplayMu.Unlock()
	s.Display.Term.Refresh()
	return true
}

func (s *Session) cmdExpand(text string) bool {
	if !(text == "/expand" || text == "/xpan" || strings.HasPrefix(text, "/expand ") || strings.HasPrefix(text, "/xpan ")) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "/expand"), "/xpan"))
	height, _, err := parseFindArg(rest)
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("| [Local]: Dùng /expand #height (vd /expand 1234).\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	s.Display.DisplayMu.Lock()
	wire, ok := s.Chain.WireIdx.get(height)
	if !ok {
		s.emitLocalFeedback("| [Local]: Tin đã trôi khỏi bộ nhớ hoặc chưa sync.\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	if findCollapsed(s.Display.TabChat.lines, height) < 0 {
		s.emitLocalFeedback("| [Local]: Tin này không thu gọn.\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	// Re-render full and replay it inside a dim heredoc frame
	// (like shell <<EOF): the delimiters mark history replay
	// without touching the verbatim content. No buffer surgery.
	s.Verify.AutoVerifyMu.RLock()
	av := s.Verify.AutoVerify
	s.Verify.AutoVerifyMu.RUnlock()
	s.Display.ShowMetaMu.RLock()
	withMeta := s.Display.ShowMeta
	s.Display.ShowMetaMu.RUnlock()
	_, full, _, _, _ := s.buildChatBlock(wire, av, withMeta)
	s.emitLocalFeedback(fmt.Sprintf("| [Local]: \x1b[90m<<<<<<< #%d\x1b[0m\n", height))
	for _, line := range strings.Split(strings.TrimSuffix(full, "\n"), "\n") {
		s.emitLocalFeedback(line + "\n")
	}
	s.emitLocalFeedback(fmt.Sprintf("| [Local]: \x1b[90m>>>>>>> #%d\x1b[0m\n", height))
	s.Display.DisplayMu.Unlock()
	s.Display.Term.Refresh()
	return true
}

func (s *Session) cmdInfo(text string) bool {
	if !(text == "/info" || strings.HasPrefix(text, "/info ")) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/info"))
	height, suffix, err := parseFindArg(rest)
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("| [Local]: Dùng /info <height>[:hash] (vd /info 1234).\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	s.Display.DisplayMu.Lock()
	wire, ok := s.Chain.WireIdx.get(height)
	if !ok {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ (legacy không có metadata, hoặc đã evict).\n", height))
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	if suffix != "" && !strings.HasPrefix(strings.ToLower(wire.ChainHash), suffix) {
		s.emitLocalFeedback("| [Local]: Height đúng nhưng hash khác — kiểm tra lại số.\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	for _, line := range formatInfoBlock(wire) {
		s.emitLocalFeedback(line)
	}
	s.Display.DisplayMu.Unlock()
	s.Display.Term.Refresh()
	return true
}

func (s *Session) cmdCopy(text string) bool {
	if !(text == "/copy" || strings.HasPrefix(text, "/copy ")) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/copy"))
	height, _, err := parseFindArg(rest)
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("| [Local]: Dùng /copy <height>[:hash] (vd /copy 1234).\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	s.Display.DisplayMu.Lock()
	wire, ok := s.Chain.WireIdx.get(height)
	if !ok {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ (legacy không có metadata, hoặc đã evict).\n", height))
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	if err := copyToClipboard(wire.Text); err != nil {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: %v.\n", err))
	} else {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Đã copy nội dung tin #%d.\n", height))
		scheduleClipboardClear(wire.Text, ClientCfg.ClipboardClearAfterSec())
	}
	s.Display.DisplayMu.Unlock()
	s.Display.Term.Refresh()
	return true
}

func (s *Session) cmdClearhistory(text string) bool {
	if !(text == "/clearhistory" || text == "/ch") {
		return false
	}
	os.Remove(historyFile)
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback(fmt.Sprintf("🗑️ Đã xóa file lịch sử gõ phím tại: %s\n", historyFile))
	s.Display.DisplayMu.Unlock()
	return true
}

// cmdOlder fetches an older history segment on demand: the request
// carries the oldest height in memory, so the server answers with the
// window right below it. The segment returns in replay format and the
// pump renders it like the join burst.
func (s *Session) cmdOlder(text string) bool {
	if !(text == "/older" || strings.HasPrefix(text, "/older ")) {
		return false
	}
	limit := 50
	if rest := strings.TrimSpace(strings.TrimPrefix(text, "/older")); rest != "" {
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 {
			s.Display.DisplayMu.Lock()
			s.emitLocalFeedback("| [Local]: Dùng /older [số dòng] (vd /older 50).\n")
			s.Display.DisplayMu.Unlock()
			s.Display.Term.Refresh()
			return true
		}
		limit = n
	}
	s.Display.DisplayMu.Lock()
	before, ok := s.Chain.WireIdx.oldest()
	s.Display.DisplayMu.Unlock()
	if !ok {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("| [Local]: Chưa có tin nào trong bộ nhớ — không có gì để lùi.\n")
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return true
	}
	if err := s.Conn.WriteJSON(HistoryRequest{Type: "history_request", Before: before, Limit: limit}); err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Không gửi được yêu cầu lịch sử cũ: %v.\n", err))
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
	}
	return true
}
