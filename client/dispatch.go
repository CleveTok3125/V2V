package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/lib/wire"
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
		s.cmdAutoverify, s.cmdNotify, s.cmdTab, s.cmdClear,
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
		s.refreshTerminal()
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
	// UniqueEditLine makes readline erase the draft when a line is submitted,
	// but the interrupt path only moves to the end of the line and resets:
	// the half-typed row is still on screen with the cursor at its end, so
	// the notice below would join it. The input row is never a buffer entry,
	// so nothing else can account for it.
	s.clearInputRow()
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
		s.refreshTerminal()
		return cmdDone
	}
	s.Display.Term.SetPrompt("| > ")
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback("| [Local]: Ctrl+C chỉ hủy dòng nhập, thoát app bằng Ctrl+D.\n")
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
	return cmdDone
}

// cancelReply drops a pending reply draft or one-shot target. It never touches
// the sink, so the render loop can call it without deadlocking on a send.
func (s *Session) cancelReply() bool {
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	if s.Pending.ReplyDraft == 0 && s.Pending.PendingReplyTo == 0 {
		return false
	}
	s.Pending.ReplyDraft = 0
	s.Pending.PendingReplyTo = 0
	return true
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
	s.quitOnce.Do(func() {
		s.Quitting <- true
		s.flushChainTip()
		s.SendMu.Lock()
		s.Conn.WriteMessage(wsCloseMessage, []byte{})
		s.SendMu.Unlock()
		if s.TripPriv != nil {
			for i := range s.TripPriv {
				s.TripPriv[i] = 0
			}
		}
		s.Display.DisplayMu.Lock()
		s.releaseCatchupLocked()
		s.flushPendingGreetingLocked()
		s.Display.DisplayMu.Unlock()
		if s.Display.Sink == nil {
			s.enqueueOutput("| [Local]: 👋 Đang ngắt kết nối... Tạm biệt!\n")
		}
		// Let the goodbye flush and the pump tear down instead of racing
		// them: return as soon as the pump exits, same 500ms cap as the
		// old fixed sleep when it never does.
		if s.PumpDone != nil {
			select {
			case <-s.PumpDone:
			case <-time.After(500 * time.Millisecond):
			}
		}
		// Drain the terminal queue so no queued line is lost on exit.
		s.flushOutputNow()
		if s.Display.Sink != nil {
			// The app stops the run loop; main restores the terminal.
			s.Display.Sink.Quit()
			return
		}
		notifyQuit()
	})
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

// helpLine is one /help row: a usage and a one-line description. A row with
// no usage is prose and takes no column.
type helpLine struct {
	usage string
	desc  string
}

// helpUsageCap bounds the usage column. The table has one usage far longer than
// the rest; letting it set the column would push every description past the
// screen edge, so that row breaks out of the column instead.
const helpUsageCap = 30

// helpLines is the command table, in reading order.
func helpLines() []helpLine {
	return []helpLine{
		{"/help, /h", "Hiển thị bảng trợ giúp này"},
		{"/clear, /c", "Xóa sạch màn hình chat"},
		{"/clearhistory, /ch", "Xóa file lịch sử gõ phím lưu trên máy"},
		{"/quit, /q", "Rời phòng chat và tắt ứng dụng"},
		{"/showjoin, /sj", "Bật/tắt hiện thông báo người khác ra vào phòng cho các tin kế tiếp"},
		{"/whoami, /w", "Thông tin danh tính và quyền hiện tại"},
		{"/status", "Trạng thái kết nối và phiên bản client"},
		{"/autoverify, /av", "Bật/tắt auto-verify trip (mặc định BẬT, queue FIFO, verify song song)"},
		{"/notify, /nt [<tag> on|off | all on|off | powmin <N>]", "Ẩn/hiện thông báo theo tag"},
		{"/info <n>[:hash]", "Xem đầy đủ metadata tin nhắn (verify lại tại local)"},
		{"/expand <n>, /xpan", "Mở đầy đủ tin bị thu gọn (vd /expand 1234)"},
		{"/copy <n>[:hash]", "Copy nội dung thô tin nhắn vào clipboard"},
		{"/tab, /t [1|2]", "Chuyển tab chat / local & system"},
		{"/meta, /m [on|off]", "Hiện/ẩn dòng meta #height:hash (mặc định hiện, chain vẫn verify)"},
		{"/find, /f <n>[:hash]", "Tìm tin theo số height trong bộ nhớ (vd /find 1234)"},
		{"/older [n]", "Lấy thêm n tin cũ hơn từ server (mặc định 50)"},
		{"/reply <n>[:hash] text", "Trả lời tin #n kèm quote (vd /reply 1234 đồng ý)"},
		{"/reply <n>", "Soạn reply nháp, dòng tiếp theo là nội dung"},
		{desc: "Gõ @#n (vd @#1234) trong tin để nhắc tới tin khác (sáng lên khi còn trong bộ nhớ)"},
		{desc: "Lệnh lạ bắt đầu bằng / bị chặn, không gửi đi (muốn gửi chữ / đầu dòng thì dùng codeblock)"},
		{desc: "Gõ ``` ở đầu và cuối tin nhắn để gửi Code block / nhiều dòng (^C hủy nhập)"},
		{desc: "Bọc chữ trong `dấu backtick` để hiện nền riêng (inline code một dòng)"},
	}
}

func (s *Session) cmdHelp(text string) bool {
	if !(text == "/help" || text == "/h") {
		return false
	}
	lines := helpLines()
	width := 0
	for _, l := range lines {
		if n := len(l.usage); n > width && n <= helpUsageCap {
			width = n
		}
	}
	rows := make([]string, 0, len(lines)+1)
	rows = append(rows, "| [Local]: Trợ giúp — danh sách lệnh:")
	for _, l := range lines {
		if l.usage == "" {
			rows = append(rows, "|   - "+l.desc)
			continue
		}
		rows = append(rows, fmt.Sprintf("|   - %-*s: %s", width, l.usage, l.desc))
	}
	s.Display.DisplayMu.Lock()
	// One entry: every entry carries a trailing gap row, so a row per entry
	// would put a blank row between each pair.
	s.emitLocalFeedback(strings.Join(rows, "\n"))
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

// notifyTagWidth is the room the tag tree gives each tag before its state,
// indent included. Counting the indent against this budget is what keeps the
// states in one column as the tree deepens.
const notifyTagWidth = 28

// notifyTagRow renders one tag-tree row with the state in a fixed column.
func notifyTagRow(tag, state string) string {
	indent := strings.Repeat("  ", strings.Count(tag, "."))
	width := notifyTagWidth - len(indent)
	if width < 1 {
		width = 1
	}
	return fmt.Sprintf("|   %s%-*s %s", indent, width, tag, state)
}

// cmdNotify lists or toggles the informational notice gates. Muting a
// kind hides it live while it still lands in the system tab.
func (s *Session) cmdNotify(text string) bool {
	if !(text == "/notify" || text == "/nt" || strings.HasPrefix(text, "/notify ") || strings.HasPrefix(text, "/nt ")) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/notify"))
	if rest == text {
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/nt"))
	}
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	fields := strings.Fields(rest)
	if len(fields) > 0 {
		fields[0] = strings.ToLower(fields[0])
	}
	if len(fields) == 0 {
		rows := make([]string, 0, len(wire.AllTags())+4)
		rows = append(rows, "| [Local]: Thông báo (tag → trạng thái):")
		for _, tag := range wire.AllTags() {
			state := onOffLabel(s.notifyTagEnabled(tag))
			// Indent by depth so the subtree reads as one, and show the
			// gate that actually applies: a child of a muted parent is off
			// whether or not its own key is set.
			rows = append(rows, notifyTagRow(tag, state))
		}
		rows = append(rows,
			fmt.Sprintf("|   powmin = %d", s.notifyPowMinTier()),
			"|   Dùng /notify <tag> on|off | all on|off | powmin <N> (off: ẩn live, vẫn lưu tab 2)",
			"|   powmin ẩn thông báo PoW trong phòng; thông báo PoW lúc vào cửa theo ui.powMinTier",
		)
		// The listing is one entry: every row carries a trailing gap row
		// otherwise, which would space the tree out.
		s.emitLocalFeedback(strings.Join(rows, "\n"))
		return true
	}
	if fields[0] == "powmin" {
		if len(fields) != 2 {
			s.emitLocalFeedback("| [Local]: Dùng /notify powmin <N> (N >= 1).\n")
			return true
		}
		tier, err := strconv.Atoi(fields[1])
		if err != nil || tier < 1 {
			s.emitLocalFeedback("| [Local]: powmin phải là số nguyên >= 1.\n")
			return true
		}
		s.notifySetPowMinTier(tier)
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Notify powmin = %d (áp dụng từ giờ cho PoW trong phòng).\n", tier))
		return true
	}
	if len(fields) != 2 {
		s.emitLocalFeedback("| [Local]: Dùng /notify <tag> on|off | all on|off | powmin <N>.\n")
		return true
	}
	on, ok := parseOnOff(fields[1])
	if !ok {
		s.emitLocalFeedback("| [Local]: Giá trị phải là on hoặc off.\n")
		return true
	}
	if fields[0] == "all" {
		s.notifySetAll(on)
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Notify all = %s.\n", onOffLabel(on)))
		return true
	}
	if !s.notifySetTag(fields[0], on) {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Không rõ tag thông báo: %s (xem /notify)\n", fields[0]))
		return true
	}
	s.emitLocalFeedback(fmt.Sprintf("| [Local]: Notify %s = %s.\n", fields[0], onOffLabel(on)))
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
	// switchTab paints the bar itself, inside the same ordered chunk as the
	// dump, so nothing is queued behind the prompt it would race.
	s.switchTab(n)
	return true
}

func (s *Session) cmdClear(text string) bool {
	if !(text == "/clear" || text == "/c") {
		return false
	}
	s.Display.DisplayMu.Lock()
	s.releaseCatchupLocked()
	s.Display.PendingGreeting = ""
	if s.Display.Sink != nil {
		s.Display.TabChat.reset()
		banner := "Đã kết nối với username: " + serverField(s.Username) + "\nGõ tin nhắn để chat, /help để hiện trợ giúp\n\n"
		s.Display.TabChat.append(noticeEntry(strings.TrimSuffix(banner, "\n")))
		s.Display.Sink.Notify()
		s.Display.DisplayMu.Unlock()
		return true
	}
	s.Display.DisplayMu.Unlock()
	s.enqueueOutput("\033[H\033[2J")
	s.enqueueOutput("Đã kết nối với username: " + serverField(s.Username) + "\nGõ tin nhắn để chat, /help để hiện trợ giúp\n\n")
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
		s.refreshTerminal()
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
		s.refreshTerminal()
		return text, cmdDone, true
	}
	s.Display.DisplayMu.Lock()
	found := entryIndexByHeight(s.Display.TabChat.lines, height, suffix) >= 0 ||
		entryIndexByHeight(s.Display.TabSys.lines, height, suffix) >= 0
	s.Display.DisplayMu.Unlock()
	if !found {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ, không reply được.\n", height))
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return text, cmdDone, true
	}
	if body == "" {
		// Draft mode: quote now, body on the next line. Any slash
		// command or empty-line ^C aborts it (see loop top).
		s.Pending.ReplyDraft = height
		s.Display.Term.SetPrompt(fmt.Sprintf("↩ #%d > ", height))
		s.Display.DisplayMu.Lock()
		if s.Display.Sink == nil {
			// The legacy printer writes the preview and its hint itself; the
			// TUI shows the preview above the input box and the hint in the
			// status line.
			for _, q := range s.quoteLinesFor(height, false) {
				fmt.Fprint(s.Display.Out, q+"\n")
				s.Display.PrintGen++
			}
			s.emitLocalFeedback("| [Local]: Gõ nội dung reply (Enter gửi, ^C ở dòng trống hủy, lệnh / khác hủy draft).\n")
		}
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
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
		s.refreshTerminal()
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
	s.refreshTerminal()
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
		s.refreshTerminal()
		return true
	}
	s.Display.DisplayMu.Lock()
	shown := 0
	header := fmt.Sprintf("| [Local]: Tìm #%d", height)
	if suffix != "" {
		header += ":" + suffix
	}
	rows := []string{header + " trong bộ nhớ:"}
	// Snapshot matches before emitting: emitting appends to
	// sess.Display.TabSys, whose eviction could shift indices mid-scan.
	var hits []string
	for _, buf := range []*tabBuffer{s.Display.TabChat, s.Display.TabSys} {
		if idx := entryIndexByHeight(buf.lines, height, suffix); idx >= 0 {
			// One entry holds the whole message, so there is no second row
			// to pair it with. Its rendered text ends with the gap row, which
			// the join would double.
			hits = append(hits, strings.TrimRight(buf.lines[idx].text(), "\n"))
			shown++
		}
	}
	// Replay inside the same dim heredoc frame as /expand so replayed
	// lines never read as live chat. No buffer surgery: markers only.
	if shown > 0 {
		rows = append(rows, fmt.Sprintf("| [Local]: %s", heredocMarker("<<<<<<<", height)))
	}
	rows = append(rows, hits...)
	if shown == 0 {
		rows = append(rows, "| [Local]: Không thấy (tin cũ đã bị evict khỏi bộ nhớ hoặc chưa sync).")
	} else {
		rows = append(rows, fmt.Sprintf("| [Local]: %s", heredocMarker(">>>>>>>", height)))
	}
	// One entry, so the frame and the lines it wraps stay together.
	s.emitLocalFeedback(strings.Join(rows, "\n"))
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
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
		s.refreshTerminal()
		return true
	}
	s.Display.DisplayMu.Lock()
	wire, ok := s.Chain.WireIdx.get(height)
	if !ok {
		s.emitLocalFeedback("| [Local]: Tin đã trôi khỏi bộ nhớ hoặc chưa sync.\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	if collapsedIndex(s.Display.TabChat.lines, height) < 0 {
		s.emitLocalFeedback("| [Local]: Tin này không thu gọn.\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
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
	full, _ := s.buildChatBlock(wire, av, withMeta)
	// One entry, so the frame and the block it wraps stay together.
	s.emitLocalFeedback(strings.Join([]string{
		fmt.Sprintf("| [Local]: %s", heredocMarker("<<<<<<<", height)),
		strings.TrimRight(full.text(), "\n"),
		fmt.Sprintf("| [Local]: %s", heredocMarker(">>>>>>>", height)),
	}, "\n"))
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
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
		s.refreshTerminal()
		return true
	}
	s.Display.DisplayMu.Lock()
	wire, ok := s.Chain.WireIdx.get(height)
	if !ok {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ (legacy không có metadata, hoặc đã evict).\n", height))
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	if suffix != "" && !strings.HasPrefix(strings.ToLower(wire.ChainHash), suffix) {
		s.emitLocalFeedback("| [Local]: Height đúng nhưng hash khác — kiểm tra lại số.\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	// One entry: the rows already end with a newline, so joining them empty
	// keeps the block together and trimming drops the last one.
	s.emitLocalFeedback(strings.TrimRight(strings.Join(formatInfoBlock(wire), ""), "\n"))
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
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
		s.refreshTerminal()
		return true
	}
	s.Display.DisplayMu.Lock()
	wire, ok := s.Chain.WireIdx.get(height)
	if !ok {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ (legacy không có metadata, hoặc đã evict).\n", height))
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	if err := copyToClipboard(wire.Text); err != nil {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: %v.\n", err))
	} else {
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Đã copy nội dung tin #%d.\n", height))
		scheduleClipboardClear(wire.Text, ClientCfg.ClipboardClearAfterSec())
	}
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
	return true
}

func (s *Session) cmdClearhistory(text string) bool {
	if !(text == "/clearhistory" || text == "/ch") {
		return false
	}
	os.Remove(historyFile)
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback(fmt.Sprintf("| [Local]: 🗑️ Đã xóa file lịch sử gõ phím tại: %s\n", historyFile))
	s.Display.DisplayMu.Unlock()
	return true
}

// cmdOlder fetches an older history segment on demand: the request
// carries the oldest height in memory, so the server answers with the
// window right below it. The segment returns in replay format and the
// pump renders it like the join burst.
// requestOlder asks the server for the window below the oldest height in
// memory. Reports ok=false when nothing is in memory to page below.
func (s *Session) requestOlder(limit int) (bool, error) {
	s.Display.DisplayMu.Lock()
	before, ok := s.Chain.WireIdx.oldest()
	s.Display.DisplayMu.Unlock()
	if !ok {
		return false, nil
	}
	return true, s.sendJSON(HistoryRequest{Type: "history_request", Before: before, Limit: limit})
}

// olderAvailable reports whether a page-up at the top could return more
// history: something is in memory, it starts above height 1, and no older
// segment is already in flight.
func (s *Session) olderAvailable() bool {
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	oldest, ok := s.Chain.WireIdx.oldest()
	return ok && oldest > 1 && !s.Chain.InOlder && !s.Chain.OlderExhausted
}

// uiStatus is a short footer status for in-flight background loads.
func (s *Session) uiStatus() string {
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	switch {
	case s.Chain.InOlder:
		return "Đang tải lịch sử cũ…"
	case s.Chain.InRecover:
		return "Đang khôi phục tin bị thiếu…"
	case s.Pending.ReplyDraft > 0:
		return "reply nháp: Enter gửi, ^C hủy"
	default:
		return ""
	}
}

// emitOlderNotice is a local line for the /older paths, which run outside
// the input loop and must take DisplayMu themselves.
func (s *Session) emitOlderNotice(line string) {
	s.Display.DisplayMu.Lock()
	s.emitLocalFeedback(line)
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
}

func (s *Session) cmdOlder(text string) bool {
	if !(text == "/older" || strings.HasPrefix(text, "/older ")) {
		return false
	}
	limit := 50
	if rest := strings.TrimSpace(strings.TrimPrefix(text, "/older")); rest != "" {
		n, err := strconv.Atoi(rest)
		if err != nil || n <= 0 {
			s.emitOlderNotice("| [Local]: Dùng /older [số dòng] (vd /older 50).\n")
			return true
		}
		limit = n
	}
	ok, err := s.requestOlder(limit)
	if !ok {
		s.emitOlderNotice("| [Local]: Chưa có tin nào trong bộ nhớ — không có gì để lùi.\n")
		return true
	}
	if err != nil {
		s.emitOlderNotice(fmt.Sprintf("| [Local]: Không gửi được yêu cầu lịch sử cũ: %v.\n", err))
	}
	return true
}
