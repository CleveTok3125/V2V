package main

import (
	"os"
	"testing"
)

func TestAssessFilePassphrase(t *testing.T) {
	weak := assessFilePassphrase("123")
	if !weak.Weak || weak.Label != "yếu" {
		t.Errorf("assessFilePassphrase(123) = %+v, want weak/yếu", weak)
	}
	strong := assessFilePassphrase("correct horse battery staple radio tower")
	if strong.Weak || strong.Bits <= 0 {
		t.Errorf("assessFilePassphrase(long) = %+v, want non-weak with bits", strong)
	}
	if strong.Bits > 128 {
		t.Errorf("bits must cap at 128, got %v", strong.Bits)
	}
	huge := assessFilePassphrase("correct horse battery staple radio tower antenna satellite ocean mountain river valley forest desert")
	if !huge.Capped || huge.Bits != 128 {
		t.Errorf("assessFilePassphrase(huge) = %+v, want Capped at 128", huge)
	}
}

// withPipedStdin swaps os.Stdin for a pipe feeding input, restoring it
// after the test. passprompt detects the pipe as non-TTY and takes the
// plain-line fallback.
func withPipedStdin(t *testing.T, input string) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
	})
}

func TestPromptPassphrasePiped(t *testing.T) {
	withPipedStdin(t, "same phrase here\nsame phrase here\n")
	got, err := promptPassphrase()
	if err != nil || got != "same phrase here" {
		t.Errorf("promptPassphrase = %q, %v", got, err)
	}
}

func TestPromptPassphraseForLoadRetry(t *testing.T) {
	withPipedStdin(t, "\nsecret\n")
	got, err := promptPassphraseForLoad()
	if err != nil || got != "secret" {
		t.Errorf("promptPassphraseForLoad = %q, %v, want retry past blank", got, err)
	}
}
