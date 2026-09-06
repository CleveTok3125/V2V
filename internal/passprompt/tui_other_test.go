//go:build !js

package passprompt

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRenderMeterLine(t *testing.T) {
	line := renderMeterLine(Assessment{Bits: 42, Label: "mạnh"})
	if strings.Contains(line, "📊") {
		t.Errorf("meter must not carry the 📊 prefix: %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("meter must be a single line: %q", line)
	}
	if !strings.Contains(line, "42 bits — mạnh") {
		t.Errorf("meter missing bits + label: %q", line)
	}
	if strings.Count(line, "█")+strings.Count(line, "░") != 10 {
		t.Errorf("meter missing 10-cell bar: %q", line)
	}
	frac := renderMeterLine(Assessment{Bits: 12.34, Label: "yếu", Weak: true})
	if !strings.Contains(frac, "12.3 bits — yếu") {
		t.Errorf("fractional bits: %q", frac)
	}
}

func TestMeterColorBands(t *testing.T) {
	cases := []struct {
		in   Assessment
		want string
	}{
		{Assessment{Bits: 99, Label: "x", Weak: true}, "1"},
		{Assessment{Bits: 0, Label: "yếu", Weak: true}, "1"},
		{Assessment{Bits: 10}, "3"},
		{Assessment{Bits: 49.9}, "3"},
		{Assessment{Bits: 50}, "2"},
		{Assessment{Bits: 60}, "2"},
		{Assessment{Bits: 79.9}, "2"},
		{Assessment{Bits: 80}, "6"},
		{Assessment{Bits: 128}, "6"},
	}
	for _, c := range cases {
		if got := string(meterColor(c.in)); got != c.want {
			t.Errorf("meterColor(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMeterBarBitsMapping(t *testing.T) {
	cases := []struct {
		bits float64
		full int
	}{
		{0, 0}, {64, 5}, {128, 10}, {200, 10},
	}
	for _, c := range cases {
		line := renderMeterLine(Assessment{Bits: c.bits, Label: "x"})
		if got := strings.Count(line, "█"); got != c.full {
			t.Errorf("bits %v: full cells = %d, want %d (%q)", c.bits, got, c.full, line)
		}
	}
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func keyType(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// drive feeds keys through Update and returns the final passwordModel.
func drive(m passwordModel, keys []tea.KeyMsg) passwordModel {
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(passwordModel)
		if m.done {
			break
		}
	}
	return m
}

func typeKeys(s string) []tea.KeyMsg {
	var out []tea.KeyMsg
	for _, r := range s {
		out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return out
}

func enter() []tea.KeyMsg { return []tea.KeyMsg{keyType(tea.KeyEnter)} }

func assessStub(label string) func(string) Assessment {
	return func(s string) Assessment {
		return Assessment{Bits: float64(len(s)), Label: label}
	}
}

func TestPasswordModelLiveAssess(t *testing.T) {
	calls := 0
	opts := PasswordOpts{
		Title:        "Nhập",
		ConfirmTitle: "Nhập lại",
		Assess: func(s string) Assessment {
			calls++
			return Assessment{Bits: float64(len(s)), Label: "yếu", Weak: true}
		},
	}
	m := newPasswordModel(opts)
	m = drive(m, typeKeys("abc"))
	if calls == 0 {
		t.Fatal("assess never ran while typing: meter is not live")
	}
	if m.snap.Bits != 3 {
		t.Errorf("snap.Bits = %v, want 3", m.snap.Bits)
	}
	view := m.View()
	if !strings.Contains(view, "3 bits — yếu") {
		t.Errorf("view missing live meter:\n%s", view)
	}
	if strings.Contains(view, "📊") {
		t.Errorf("meter must not carry the 📊 prefix:\n%s", view)
	}
	if strings.Contains(view, "Nhập lại") {
		t.Error("confirm title must not show during entry phase")
	}
}

func TestViewOrder(t *testing.T) {
	m := drive(newPasswordModel(PasswordOpts{Title: "Nhập", Assess: assessStub("mạnh")}), typeKeys("ab"))
	view := m.View()
	iTitle := strings.Index(view, "Nhập")
	iMeter := strings.Index(view, "bits —")
	iInput := strings.Index(view, "> ")
	if iTitle < 0 || iMeter < 0 || iInput < 0 {
		t.Fatalf("view missing parts:\n%s", view)
	}
	if !(iTitle < iMeter && iMeter < iInput) {
		t.Errorf("order must be title < meter < input (got %d < %d < %d):\n%s", iTitle, iMeter, iInput, view)
	}
}

func TestPasswordModelExpect(t *testing.T) {
	newExpect := func() passwordModel {
		return newPasswordModel(PasswordOpts{ConfirmTitle: "Nhập lại", MaxRounds: 2, Expect: "secret"})
	}
	if m := newExpect(); m.phase != 1 || m.first != "secret" {
		t.Fatalf("expect mode must start at confirmation (phase=%d)", m.phase)
	}
	keys := append(typeKeys("secret"), enter()...)
	m := drive(newExpect(), keys)
	if !m.done || m.aborted || m.fail != nil || m.value != "secret" {
		t.Fatalf("match: done=%v aborted=%v fail=%v value=%q", m.done, m.aborted, m.fail, m.value)
	}
	var keys2 []tea.KeyMsg
	for i := 0; i < 2; i++ {
		keys2 = append(keys2, typeKeys("x")...)
		keys2 = append(keys2, enter()...)
	}
	m2 := drive(newExpect(), keys2)
	if !m2.done || m2.fail != ErrMismatch {
		t.Errorf("exhaustion: done=%v fail=%v, want ErrMismatch", m2.done, m2.fail)
	}
	m3 := drive(newExpect(), typeKeys("sec"))
	if view := m3.View(); strings.Contains(view, "bits") {
		t.Errorf("confirm field must not show a meter:\n%s", view)
	}
	if !strings.Contains(m3.View(), "Nhập lại") {
		t.Errorf("expect mode must show the confirm title:\n%s", m3.View())
	}
	m4 := drive(newExpect(), []tea.KeyMsg{keyType(tea.KeyEsc)})
	if !m4.done || !m4.aborted {
		t.Errorf("esc: done=%v aborted=%v", m4.done, m4.aborted)
	}
}

func TestAllowEmptyConfirm(t *testing.T) {
	opts := PasswordOpts{Title: "Nhập", ConfirmTitle: "Nhập lại", Confirm: true, AllowEmpty: true}
	m := drive(newPasswordModel(opts), enter())
	if !m.done || m.aborted || m.value != "" {
		t.Errorf("empty-first with AllowEmpty: done=%v aborted=%v value=%q", m.done, m.aborted, m.value)
	}
}

func TestPasswordModelDoubleEntry(t *testing.T) {
	opts := PasswordOpts{Title: "Nhập", ConfirmTitle: "Nhập lại", Confirm: true, Assess: assessStub("mạnh")}
	keys := append(typeKeys("secret"), enter()...)
	keys = append(keys, typeKeys("secret")...)
	keys = append(keys, enter()...)
	m := drive(newPasswordModel(opts), keys)
	if !m.done || m.aborted || m.fail != nil {
		t.Fatalf("done=%v aborted=%v fail=%v", m.done, m.aborted, m.fail)
	}
	if m.value != "secret" {
		t.Errorf("value = %q, want secret", m.value)
	}
}

func TestPasswordModelMismatchRetryCap(t *testing.T) {
	opts := PasswordOpts{Title: "Nhập", ConfirmTitle: "Nhập lại", Confirm: true, MaxRounds: 2}
	var keys []tea.KeyMsg
	for i := 0; i < 2; i++ {
		keys = append(keys, typeKeys("a")...)
		keys = append(keys, enter()...)
		keys = append(keys, typeKeys("b")...)
		keys = append(keys, enter()...)
	}
	m := drive(newPasswordModel(opts), keys)
	if !m.done || m.fail != ErrMismatch {
		t.Errorf("exhaustion: done=%v fail=%v, want ErrMismatch", m.done, m.fail)
	}
}

func TestPasswordModelEmptyRejected(t *testing.T) {
	m := drive(newPasswordModel(PasswordOpts{Title: "Nhập"}), enter())
	if m.done {
		t.Error("empty submit must not finish when AllowEmpty=false")
	}
	if m.errMsg == "" {
		t.Error("empty submit must show an error")
	}
	m2 := drive(newPasswordModel(PasswordOpts{Title: "Nhập", AllowEmpty: true}), enter())
	if !m2.done || m2.value != "" {
		t.Errorf("AllowEmpty submit: done=%v value=%q", m2.done, m2.value)
	}
}

func TestPasswordModelEscAborts(t *testing.T) {
	m := drive(newPasswordModel(PasswordOpts{Title: "Nhập"}), []tea.KeyMsg{keyType(tea.KeyEsc)})
	if !m.done || !m.aborted {
		t.Errorf("esc: done=%v aborted=%v", m.done, m.aborted)
	}
}

// driveConfirm feeds keys and returns the final confirmModel.
func driveConfirm(m confirmModel, keys []tea.KeyMsg) confirmModel {
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(confirmModel)
		if m.done {
			break
		}
	}
	return m
}

func TestConfirmModelDefaultNo(t *testing.T) {
	// Fresh model focuses Không: Enter alone answers No.
	m := driveConfirm(confirmModel{}, enter())
	if !m.done || m.yes {
		t.Errorf("default: done=%v yes=%v, want done=true yes=false", m.done, m.yes)
	}
	// Right moves to Có, Enter accepts.
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyType(tea.KeyRight), keyType(tea.KeyEnter)})
	if !m.done || !m.yes {
		t.Errorf("right+enter: done=%v yes=%v, want yes=true", m.done, m.yes)
	}
	// y answers Có immediately; n answers Không.
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyRunes("y")})
	if !m.done || !m.yes {
		t.Errorf("y: done=%v yes=%v", m.done, m.yes)
	}
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyRunes("n")})
	if !m.done || m.yes {
		t.Errorf("n: done=%v yes=%v", m.done, m.yes)
	}
	// Esc aborts with an error, never a silent No.
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyType(tea.KeyEsc)})
	if !m.done || !m.aborted {
		t.Errorf("esc: done=%v aborted=%v", m.done, m.aborted)
	}
	view := confirmModel{}.View()
	if !strings.Contains(view, "Có") || !strings.Contains(view, "Không") {
		t.Errorf("view missing options:\n%s", view)
	}
}

func TestConfirmModelKeys(t *testing.T) {
	// Left from default-No stays No (toggle twice returns).
	m := driveConfirm(confirmModel{}, []tea.KeyMsg{keyType(tea.KeyLeft), keyType(tea.KeyLeft), keyType(tea.KeyEnter)})
	if !m.done || m.yes {
		t.Errorf("left+left+enter: done=%v yes=%v, want yes=false", m.done, m.yes)
	}
	// h/l toggle like arrows.
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyRunes("l"), keyType(tea.KeyEnter)})
	if !m.done || !m.yes {
		t.Errorf("l+enter: done=%v yes=%v, want yes=true", m.done, m.yes)
	}
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyRunes("l"), keyRunes("h"), keyType(tea.KeyEnter)})
	if !m.done || m.yes {
		t.Errorf("l+h+enter: done=%v yes=%v, want yes=false", m.done, m.yes)
	}
	// Ctrl+C aborts, never answers.
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyType(tea.KeyCtrlC)})
	if !m.done || !m.aborted {
		t.Errorf("ctrl+c: done=%v aborted=%v", m.done, m.aborted)
	}
	// Y/N uppercase behave like lowercase.
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyRunes("Y")})
	if !m.done || !m.yes {
		t.Errorf("Y: done=%v yes=%v", m.done, m.yes)
	}
	m = driveConfirm(confirmModel{}, []tea.KeyMsg{keyRunes("N")})
	if !m.done || m.yes {
		t.Errorf("N: done=%v yes=%v", m.done, m.yes)
	}
}
