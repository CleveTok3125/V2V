//go:build !js

package passprompt

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"

	"github.com/CleveTok3125/V2V/internal/tui"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	hintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// Password prompts for a secret. On a TTY it runs the meter-capable
// program; otherwise it reads plain lines from stdin with the same
// round semantics. Titles print verbatim on the fallback path.
func Password(opts PasswordOpts) (string, error) {
	if tui.Interactive() {
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

// meterColor bands the meter bar by score, matching the label bands
// one-to-one: bar, color, and label can never contradict each other.
// The bits number stays informational only.
func meterColor(a Assessment) lipgloss.Color {
	switch {
	case a.Score <= 1:
		return lipgloss.Color("1")
	case a.Score == 2:
		return lipgloss.Color("3")
	case a.Score == 3:
		return lipgloss.Color("2")
	default:
		return lipgloss.Color("6")
	}
}

// scoreFill maps score 0-4 to bar fill linearly: empty input shows
// an empty bar, top score fills it.
func scoreFill(score int) float64 {
	if score < 0 {
		score = 0
	}
	if score > 4 {
		score = 4
	}
	return float64(score) / 4
}

// renderMeterLine is the live meter: fixed-width bar beside bits +
// label on one line, no icon prefix. Pure (no lipgloss state beyond
// the color choice) so tests pin it exactly.
func renderMeterLine(a Assessment) string {
	bar := MeterBar(scoreFill(a.Score))
	styled := lipgloss.NewStyle().Foreground(meterColor(a)).Render(bar)
	bits := FormatBits(a.Bits)
	if a.Capped {
		bits += "+"
	}
	return fmt.Sprintf("%s %s bits — %s", styled, bits, a.Label)
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
