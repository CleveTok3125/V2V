package main

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Tab indices inside the app are 0-based; the session keeps its own
// 1-based IDs (TabChat/TabSystem).
const (
	tabIndexChat = iota
	tabIndexSystem
)

// uiRefreshMsg tells the app a tab buffer changed and the active tab must
// be pulled again.
type uiRefreshMsg struct{}

// uiPromptMsg carries the input prompt the session wants shown (a reply
// target re-arms it).
type uiPromptMsg struct{ prompt string }

// uiSink is the session's handle on the running app. The session never
// writes the screen itself: it keeps its tab buffers and calls Notify,
// and the app pulls the active tab and repaints.
type uiSink interface {
	Notify()
	SetPrompt(p string)
	Quit()
}

// tuiSink implements uiSink over a Bubble Tea program. Notify is
// coalesced: while a refresh is in flight further changes fold into it,
// so a history burst wakes the app once per frame instead of per line.
type tuiSink struct {
	program *tea.Program
	started atomic.Bool

	mu      sync.Mutex
	pending bool
}

func (k *tuiSink) attach(p *tea.Program) { k.program = p }

// Notify wakes the app. It is safe to call from any goroutine and before
// the program runs: the first render pulls the whole buffer anyway, so a
// refresh dropped before start loses nothing.
func (k *tuiSink) Notify() {
	if !k.started.Load() {
		return
	}
	k.mu.Lock()
	if k.pending {
		k.mu.Unlock()
		return
	}
	k.pending = true
	p := k.program
	k.mu.Unlock()
	if p != nil {
		go p.Send(uiRefreshMsg{})
	}
}

// ack clears the in-flight flag once the app has handled a refresh.
func (k *tuiSink) ack() {
	k.mu.Lock()
	k.pending = false
	k.mu.Unlock()
}

// send delivers a message once the program is running. It never blocks the
// caller: the session calls it while holding DisplayMu, and the event loop
// takes that same lock to render, so a synchronous send could deadlock
// against a render in progress.
func (k *tuiSink) send(m tea.Msg) {
	if !k.started.Load() {
		return
	}
	if pr := k.program; pr != nil {
		go pr.Send(m)
	}
}

func (k *tuiSink) SetPrompt(p string) { k.send(uiPromptMsg{prompt: p}) }

func (k *tuiSink) Quit() {
	if pr := k.program; pr != nil {
		pr.Quit()
	}
}

// tuiTerm adapts the app to the inputTerminal the session loop expects.
// ReadLine yields the next submitted line; the app owns the screen, so
// Refresh and Writer are inert.
type tuiTerm struct {
	lines  chan string
	sink   uiSink
	closed chan struct{}
	once   sync.Once
}

func (t *tuiTerm) ReadLine() (string, error) {
	select {
	case <-t.closed:
		return "", io.EOF
	case line := <-t.lines:
		return line, nil
	}
}

func (t *tuiTerm) SetPrompt(p string) { t.sink.SetPrompt(p) }
func (t *tuiTerm) Refresh()           {}
func (t *tuiTerm) Close()             { t.once.Do(func() { close(t.closed) }) }
func (t *tuiTerm) Writer() io.Writer  { return io.Discard }

// Styles for the chrome around the chat viewport and the input box.
var (
	tabActiveStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Padding(0, 1)
	tabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Padding(0, 1)
	inputChatStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62"))
	inputCmdStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("214"))
	footerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// app is the Bubble Tea model: a tab bar, the chat viewport and the input
// box. It reads the session buffers on demand and never touches the
// terminal directly.
type app struct {
	sess  *Session
	sink  *tuiSink
	term  *tuiTerm
	lines chan string

	viewport viewport.Model
	input    textarea.Model

	active   int
	width    int
	height   int
	prompt   string
	enhanced bool
	dirty    bool
	quitting bool
}

func newApp(sess *Session) *app {
	sink := &tuiSink{}
	lines := make(chan string, 16)
	a := &app{
		sess:     sess,
		sink:     sink,
		lines:    lines,
		active:   tabIndexChat,
		dirty:    true,
		viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
	}
	a.term = &tuiTerm{lines: lines, sink: sink, closed: make(chan struct{})}

	ti := textarea.New()
	ti.Placeholder = "Nhập tin nhắn…"
	ti.ShowLineNumbers = false
	ti.CharLimit = 0
	ti.DynamicHeight = true
	ti.MinHeight = 1
	ti.MaxHeight = 6
	ti.Prompt = "> "
	ti.Focus()
	a.input = ti
	return a
}

func (a *app) Init() tea.Cmd {
	a.sink.started.Store(true)
	return nil
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.layout()
		a.dirty = true
		return a, nil
	case tea.KeyboardEnhancementsMsg:
		a.enhanced = msg.SupportsKeyDisambiguation()
		return a, nil
	case uiRefreshMsg:
		a.sink.ack()
		a.dirty = true
		return a, nil
	case uiPromptMsg:
		a.prompt = msg.prompt
		return a, nil
	case tea.KeyPressMsg:
		if handled, cmd := a.handleKey(msg); handled {
			return a, cmd
		}
	}
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	a.layout()
	return a, cmd
}

// handleKey routes the keys the app owns; anything else falls through to
// the textarea for editing.
func (a *app) handleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "ctrl+d":
		a.quitting = true
		return true, tea.Quit
	case "ctrl+c", "esc":
		if strings.TrimSpace(a.input.Value()) != "" {
			a.input.Reset()
			a.layout()
		}
		return true, nil
	case "tab":
		a.switchTab(tabIndexSystem)
		return true, nil
	case "shift+tab":
		a.switchTab(tabIndexChat)
		return true, nil
	case "enter":
		return true, a.submit()
	case "shift+enter":
		if a.enhanced {
			a.insertNewline()
			return true, nil
		}
	case "ctrl+j", "alt+enter":
		a.insertNewline()
		return true, nil
	}
	return false, nil
}

// insertNewline grows the draft by one row, since plain Enter submits.
func (a *app) insertNewline() {
	a.input.InsertString("\n")
	a.layout()
}

// submit hands the draft to the session and clears the input box.
func (a *app) submit() tea.Cmd {
	text := a.input.Value()
	a.input.Reset()
	a.layout()
	a.dirty = true
	if strings.TrimSpace(text) == "" {
		return nil
	}
	select {
	case a.lines <- text:
	default:
	}
	return nil
}

func (a *app) switchTab(i int) {
	if i == a.active {
		return
	}
	a.active = i
	a.sess.switchTab(i + 1)
	a.dirty = true
}

// layout fits the viewport between the tab bar and the input box: the
// textarea grows with the draft, so the chat shrinks to match.
func (a *app) layout() {
	if a.width <= 0 || a.height <= 0 {
		return
	}
	inputH := a.input.Height()
	if inputH < 1 {
		inputH = 1
	}
	chrome := 1 + (inputH + 2) + 1 // tabs + bordered input + footer
	vpH := a.height - chrome
	if vpH < 1 {
		vpH = 1
	}
	a.viewport.SetWidth(a.width)
	a.viewport.SetHeight(vpH)
	a.input.SetWidth(a.width - 2)
}

// refreshContent pulls the active tab and repaints the viewport. The
// session mutates its buffers under DisplayMu, so the entries are copied
// under that lock and rendered outside it.
func (a *app) refreshContent() {
	if !a.dirty {
		return
	}
	a.sess.Display.DisplayMu.Lock()
	a.active = a.sess.Display.ActiveTab - 1
	var lines []entry
	if a.active == tabIndexSystem {
		lines = append(lines, a.sess.Display.TabSys.lines...)
	} else {
		lines = append(lines, a.sess.Display.TabChat.lines...)
	}
	a.sess.Display.DisplayMu.Unlock()

	var sb strings.Builder
	for _, e := range lines {
		sb.WriteString(e.text())
	}
	a.viewport.SetContent(sb.String())
	a.viewport.GotoBottom()
	a.dirty = false
}

func (a *app) renderTabs() string {
	labels := []string{"1:chat", "2:system"}
	parts := make([]string, 0, len(labels))
	for i, l := range labels {
		if i == a.active {
			parts = append(parts, tabActiveStyle.Render(l))
			continue
		}
		parts = append(parts, tabInactiveStyle.Render(l))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func (a *app) renderInput() string {
	if strings.HasPrefix(a.input.Value(), "/") {
		a.input.Prompt = "/ "
		return inputCmdStyle.Render(a.input.View())
	}
	a.input.Prompt = "> "
	return inputChatStyle.Render(a.input.View())
}

func (a *app) renderFooter() string {
	hint := "Enter gửi • Shift+Enter xuống dòng • Tab đổi tab • Ctrl+D thoát"
	if !a.enhanced {
		hint = "Enter gửi • Alt+Enter xuống dòng • Tab đổi tab • Ctrl+D thoát"
	}
	if a.prompt != "" && a.prompt != "| > " {
		hint = strings.TrimSpace(a.prompt) + "  " + hint
	}
	return footerStyle.MaxWidth(a.width).Render(hint)
}

func (a *app) View() tea.View {
	if a.quitting {
		return tea.NewView("")
	}
	a.refreshContent()
	return tea.NewView(strings.Join([]string{
		a.renderTabs(),
		a.viewport.View(),
		a.renderInput(),
		a.renderFooter(),
	}, "\n"))
}
