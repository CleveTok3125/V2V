//go:build !js

package passprompt

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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
	if !strings.Contains(view, "3 bits") || !strings.Contains(view, "yếu") {
		t.Errorf("view missing live meter:\n%s", view)
	}
	if strings.Contains(view, "Nhập lại") {
		t.Error("confirm title must not show during entry phase")
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
