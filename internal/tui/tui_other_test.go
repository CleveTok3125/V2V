//go:build !js

package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func keyType(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// driveHuh feeds keys through a huh field model. Standalone fields
// carry a zero keymap (the form injects it at runtime), so attach the
// default map like a form would; fields only take keys while focused.
func driveHuh(m huh.Field, keys []tea.KeyMsg) tea.Model {
	m.Focus()
	var model tea.Model = m
	for _, k := range keys {
		next, _ := model.Update(k)
		model = next
	}
	return model
}

func TestHuhConfirmDefaultNo(t *testing.T) {
	newConfirm := func(yes *bool) huh.Field {
		return huh.NewConfirm().Title("Chắc chứ?").Affirmative("Có").Negative("Không").Value(yes).WithKeyMap(huh.NewDefaultKeyMap())
	}
	enter := []tea.KeyMsg{keyType(tea.KeyEnter)}
	var a bool
	driveHuh(newConfirm(&a), enter)
	if a {
		t.Error("enter alone must keep No")
	}
	var b bool
	driveHuh(newConfirm(&b), []tea.KeyMsg{keyRunes("y")})
	if !b {
		t.Error("y must answer Yes")
	}
	var c bool = true
	driveHuh(newConfirm(&c), []tea.KeyMsg{keyRunes("n")})
	if c {
		t.Error("n must answer No")
	}
	var d bool
	driveHuh(newConfirm(&d), []tea.KeyMsg{keyRunes("l")})
	if !d {
		t.Error("toggle must move focus to Yes")
	}
}

func TestHuhSelectDefaultsFirst(t *testing.T) {
	newSelect := func(choice *string) huh.Field {
		return huh.NewSelect[string]().Title("Chọn").Options(
			huh.NewOption("một", "một"),
			huh.NewOption("hai", "hai"),
		).Value(choice).WithKeyMap(huh.NewDefaultKeyMap())
	}
	enter := []tea.KeyMsg{keyType(tea.KeyEnter)}
	var untouched string
	driveHuh(newSelect(&untouched), enter)
	if untouched != "một" {
		t.Errorf("untouched select = %q, want first option", untouched)
	}
	var moved string
	driveHuh(newSelect(&moved), []tea.KeyMsg{keyType(tea.KeyDown), keyType(tea.KeyEnter)})
	if moved != "hai" {
		t.Errorf("down+enter = %q, want hai", moved)
	}
}

func TestSelectPiped(t *testing.T) {
	opts := []string{"ed25519", "passkey"}
	got, err := SelectPiped(strings.NewReader("1\n"), "Chọn", opts, 1)
	if err != nil || got != 0 {
		t.Errorf("pick 1 = %d, %v", got, err)
	}
	got, err = SelectPiped(strings.NewReader("\n"), "Chọn", opts, 1)
	if err != nil || got != 1 {
		t.Errorf("empty must yield default: %d, %v", got, err)
	}
	got, err = SelectPiped(strings.NewReader("9\n"), "Chọn", opts, 1)
	if err != nil || got != 1 {
		t.Errorf("invalid must yield default: %d, %v", got, err)
	}
	got, err = SelectPiped(strings.NewReader(""), "Chọn", opts, 0)
	if err != nil || got != 0 {
		t.Errorf("closed must yield default: %d, %v", got, err)
	}
	if _, err := SelectPiped(strings.NewReader("1\n"), "Chọn", nil, 0); err == nil {
		t.Error("empty options must fail")
	}
}
