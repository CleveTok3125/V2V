//go:build !js

package passprompt

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
	xterm "github.com/charmbracelet/x/term"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	hintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	selStyle   = lipgloss.NewStyle().Reverse(true).Bold(true)
	optStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// Interactive reports whether the full-screen prompt program can run:
// stdin must be a real terminal. Callers branch to the piped readers
// otherwise.
func Interactive() bool {
	return xterm.IsTerminal(os.Stdin.Fd())
}

// Password prompts for a secret. On a TTY it runs the meter-capable
// program; otherwise it reads plain lines from stdin with the same
// round semantics. Titles print verbatim on the fallback path.
func Password(opts PasswordOpts) (string, error) {
	if Interactive() {
		return runPassword(opts)
	}
	stdin := func() (string, error) { return ReadLine(os.Stdin) }
	if opts.Expect != "" {
		return ExpectPiped(stdin, opts.Expect, opts.rounds(), func(round, max int) {
			if round == 1 {
				fmt.Print(opts.ConfirmTitle + " ")
			} else {
				fmt.Printf("%s (lần %d/%d) ", opts.ConfirmTitle, round, max)
			}
		})
	}
	if !opts.Confirm {
		fmt.Print(opts.Title + " ")
		return SinglePiped(stdin, opts.AllowEmpty)
	}
	return DoubleEntryPiped(stdin, opts.rounds(), func(first bool, round, max int) {
		if first {
			if round == 1 {
				fmt.Print(opts.Title + " ")
			} else {
				fmt.Printf("%s (lần %d/%d) ", opts.Title, round, max)
			}
		} else {
			fmt.Print(opts.ConfirmTitle + " ")
		}
	})
}

// Confirm asks a yes/no question with a hardcoded default of No: the
// initial focus sits on Không, and piped answers need an explicit yes.
// TTY sessions get the keyboard program; otherwise one plain line.
func Confirm(title string) (bool, error) {
	if Interactive() {
		return runConfirm(title)
	}
	fmt.Print(title + " (y/N): ")
	return ConfirmPiped(os.Stdin), nil
}

// meterColor bands the meter bar. Weak is always red; the rest is a
// display-only gradient by capped bits, never a security gate.
func meterColor(a Assessment) lipgloss.Color {
	switch {
	case a.Weak:
		return lipgloss.Color("1")
	case a.Bits < 50:
		return lipgloss.Color("3")
	case a.Bits < 80:
		return lipgloss.Color("2")
	default:
		return lipgloss.Color("6")
	}
}

// renderMeterLine is the live meter: fixed-width bar beside bits +
// label on one line, no icon prefix. Pure (no lipgloss state beyond
// the color choice) so tests pin it exactly.
func renderMeterLine(a Assessment) string {
	bar := MeterBar(a.Bits / 128)
	styled := lipgloss.NewStyle().Foreground(meterColor(a)).Render(bar)
	return fmt.Sprintf("%s %s bits — %s", styled, FormatBits(a.Bits), a.Label)
}

// passwordModel is the entry + double-entry state machine. assess runs
// synchronously on every keystroke (a zxcvbn eval costs ~0.1ms); the
// snapshot renders on the next View. With opts.Expect the model starts
// in confirmation phase against the known value and never shows the
// meter.
type passwordModel struct {
	input   textinput.Model
	opts    PasswordOpts
	assess  func(string) Assessment
	snap    Assessment
	phase   int // 0 = entry, 1 = confirmation
	expect  bool // confirm-against-known-value mode (opts.Expect)
	first   string
	round   int
	errMsg  string
	done    bool
	aborted bool
	value   string
	fail    error
}

func newPasswordModel(opts PasswordOpts) passwordModel {
	ti := textinput.New()
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.Focus()
	assess := opts.Assess
	if assess == nil {
		assess = func(string) Assessment { return Assessment{} }
	}
	m := passwordModel{input: ti, opts: opts, assess: assess, round: 1}
	if opts.Expect != "" {
		m.expect, m.phase, m.first = true, 1, opts.Expect
	}
	m.snap = assess("")
	return m
}

func (m passwordModel) Init() tea.Cmd { return textinput.Blink }

func (m passwordModel) submit() passwordModel {
	v := m.input.Value()
	if m.phase == 0 {
		if strings.TrimSpace(v) == "" && !m.opts.AllowEmpty {
			m.errMsg = "❌ Không được để trống, nhập lại."
			return m
		}
		if v == "" || !m.opts.Confirm {
			m.done, m.value = true, v
			return m
		}
		m.first, m.phase, m.errMsg = v, 1, ""
		m.input.Reset()
		m.snap = m.assess("")
		return m
	}
	if v != m.first {
		if m.round >= m.opts.rounds() {
			m.done, m.fail = true, ErrMismatch
			return m
		}
		m.round++
		if m.expect {
			m.errMsg = "❌ Hai lần nhập không khớp, nhập lại."
			m.input.Reset()
			return m
		}
		m.phase, m.first, m.errMsg = 0, "", "❌ Hai lần nhập không khớp, nhập lại."
		m.input.Reset()
		m.snap = m.assess("")
		return m
	}
	if strings.TrimSpace(v) == "" {
		if m.round >= m.opts.rounds() {
			m.done, m.fail = true, ErrEmpty
			return m
		}
		m.round++
		m.phase, m.first, m.errMsg = 0, "", "❌ Không được để trống, nhập lại."
		m.input.Reset()
		m.snap = m.assess("")
		return m
	}
	m.done, m.value = true, v
	return m
}

func (m passwordModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.aborted, m.done = true, true
			return m, tea.Quit
		case tea.KeyEnter:
			m = m.submit()
			if m.done {
				return m, tea.Quit
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.phase == 0 && m.assess != nil {
		m.snap = m.assess(m.input.Value())
	}
	return m, cmd
}

func (m passwordModel) View() string {
	title := m.opts.Title
	if m.phase == 1 {
		title = m.opts.ConfirmTitle
	}
	if m.round > 1 {
		title = fmt.Sprintf("%s (lần %d/%d)", title, m.round, m.opts.rounds())
	}
	out := titleStyle.Render(title) + "\n"
	if m.opts.Assess != nil && m.phase == 0 {
		out += renderMeterLine(m.snap) + "\n"
	}
	out += m.input.View() + "\n"
	if m.errMsg != "" {
		out += errStyle.Render(m.errMsg) + "\n"
	}
	out += hintStyle.Render("Enter xác nhận • Esc hủy")
	return out
}

func runPassword(opts PasswordOpts) (string, error) {
	m, err := tea.NewProgram(newPasswordModel(opts)).Run()
	if err != nil {
		return "", err
	}
	pm := m.(passwordModel)
	if pm.aborted {
		return "", ErrAborted
	}
	if pm.fail != nil {
		return "", pm.fail
	}
	return pm.value, nil
}

// confirmModel is a two-option keyboard confirm with the initial focus
// hardcoded on Không (default No). y/Y picks Có, n/N picks Không,
// arrows or h/l move, Enter accepts, Esc aborts with an error.
type confirmModel struct {
	title   string
	yes     bool // true = focus on Có
	done    bool
	aborted bool
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if key.Type == tea.KeyCtrlC || key.Type == tea.KeyEsc {
		m.aborted, m.done = true, true
		return m, tea.Quit
	}
	switch key.Type {
	case tea.KeyEnter:
		m.done = true
		return m, tea.Quit
	case tea.KeyLeft, tea.KeyRight:
		m.yes = !m.yes
		return m, nil
	}
	switch key.String() {
	case "y", "Y":
		m.yes, m.done = true, true
		return m, tea.Quit
	case "n", "N":
		m.yes, m.done = false, true
		return m, tea.Quit
	case "h", "l":
		m.yes = !m.yes
	}
	return m, nil
}

func (m confirmModel) View() string {
	yes, no := " Có ", " Không "
	if m.yes {
		yes = selStyle.Render(yes)
		no = optStyle.Render(no)
	} else {
		yes = optStyle.Render(yes)
		no = selStyle.Render(no)
	}
	return titleStyle.Render(m.title) + "\n" + yes + "  " + no + "\n" +
		hintStyle.Render("←/→ chọn • Enter xác nhận • Esc hủy")
}

func runConfirm(title string) (bool, error) {
	m, err := tea.NewProgram(confirmModel{title: title}).Run()
	if err != nil {
		return false, err
	}
	cm := m.(confirmModel)
	if cm.aborted {
		return false, ErrAborted
	}
	return cm.yes, nil
}
