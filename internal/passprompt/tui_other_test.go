//go:build !js

package passprompt

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	if strings.Count(line, "█")+strings.Count(line, "░") != MeterWidth {
		t.Errorf("meter missing %d-cell bar: %q", MeterWidth, line)
	}
	frac := renderMeterLine(Assessment{Bits: 12.34, Label: "yếu", Weak: true})
	if !strings.Contains(frac, "12.3 bits — yếu") {
		t.Errorf("fractional bits: %q", frac)
	}
	capped := renderMeterLine(Assessment{Bits: 128, Capped: true, Label: "rất mạnh"})
	if !strings.Contains(capped, "128+ bits — rất mạnh") {
		t.Errorf("capped value must show + suffix: %q", capped)
	}
	uncapped := renderMeterLine(Assessment{Bits: 128, Label: "rất mạnh"})
	if strings.Contains(uncapped, "128+") {
		t.Errorf("uncapped value must not show + suffix: %q", uncapped)
	}
}

func TestMeterColorBands(t *testing.T) {
	cases := []struct {
		in   Assessment
		want color.Color
	}{
		{Assessment{Score: 0, Label: "yếu", Weak: true}, lipgloss.Color("1")},
		{Assessment{Score: 1, Label: "yếu", Weak: true}, lipgloss.Color("1")},
		{Assessment{Score: 2, Label: "trung bình"}, lipgloss.Color("3")},
		{Assessment{Score: 3, Label: "mạnh"}, lipgloss.Color("2")},
		{Assessment{Score: 4, Label: "rất mạnh"}, lipgloss.Color("6")},
	}
	for _, c := range cases {
		if got := meterColor(c.in); got != c.want {
			t.Errorf("meterColor(%+v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMeterScoreFill(t *testing.T) {
	cases := []struct {
		score int
		full  int
	}{
		{0, 0}, {1, 5}, {2, 10}, {3, 15}, {4, 20},
	}
	for _, c := range cases {
		line := renderMeterLine(Assessment{Score: c.score, Label: "x"})
		if got := strings.Count(line, "█"); got != c.full {
			t.Errorf("score %d: full cells = %d, want %d (%q)", c.score, got, c.full, line)
		}
	}
}

func TestMeterConsistentLabel(t *testing.T) {
	// Regression: modest bits with a top score must render a full
	// bar, never an almost-empty one next to "rất mạnh".
	line := renderMeterLine(Assessment{Bits: 41.2, Score: 4, Label: "rất mạnh"})
	if got := strings.Count(line, "█"); got != MeterWidth {
		t.Errorf("score 4 must fill the bar regardless of bits: %q", line)
	}
	if !strings.Contains(line, "41.2 bits — rất mạnh") {
		t.Errorf("bits stay informational: %q", line)
	}
}

func keyRunes(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func keyType(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c} }

// drive feeds keys through Update and returns the final passwordModel.
func drive(m passwordModel, keys []tea.KeyPressMsg) passwordModel {
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(passwordModel)
		if m.done {
			break
		}
	}
	return m
}

func typeKeys(s string) []tea.KeyPressMsg {
	var out []tea.KeyPressMsg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

func enter() []tea.KeyPressMsg { return []tea.KeyPressMsg{keyType(tea.KeyEnter)} }

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
	view := m.View().Content
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
	view := m.View().Content
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
	var keys2 []tea.KeyPressMsg
	for i := 0; i < 2; i++ {
		keys2 = append(keys2, typeKeys("x")...)
		keys2 = append(keys2, enter()...)
	}
	m2 := drive(newExpect(), keys2)
	if !m2.done || m2.fail != ErrMismatch {
		t.Errorf("exhaustion: done=%v fail=%v, want ErrMismatch", m2.done, m2.fail)
	}
	m3 := drive(newExpect(), typeKeys("sec"))
	if view := m3.View().Content; strings.Contains(view, "bits") {
		t.Errorf("confirm field must not show a meter:\n%s", view)
	}
	if !strings.Contains(m3.View().Content, "Nhập lại") {
		t.Errorf("expect mode must show the confirm title:\n%s", m3.View().Content)
	}
	m4 := drive(newExpect(), []tea.KeyPressMsg{keyType(tea.KeyEsc)})
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
	var keys []tea.KeyPressMsg
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
	m := drive(newPasswordModel(PasswordOpts{Title: "Nhập"}), []tea.KeyPressMsg{keyType(tea.KeyEsc)})
	if !m.done || !m.aborted {
		t.Errorf("esc: done=%v aborted=%v", m.done, m.aborted)
	}
}
