package main

import (
	"errors"
	"strings"
	"testing"
)

func TestCountTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"con meo ngu trua", 4},
		{"con-meo_ngu.trua", 4},
		{"a/b;c:d!e?f", 6},
		{"hello", 1},
		{"", 0},
		{"   ", 0},
		{"con mèo ngủ trưa", 4},
		{"mèo😸ngủ", 2},
		{"名古屋市", 1},
		{"abc123", 1},
		{"a1-b2", 2},
	}
	for _, c := range cases {
		if got := countTokens(c.in); got != c.want {
			t.Errorf("countTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestStrengthLabels(t *testing.T) {
	cases := map[int]string{0: "yếu", 1: "yếu", 2: "trung bình", 3: "mạnh", 4: "rất mạnh", 9: "rất mạnh"}
	for score, want := range cases {
		if got := strengthLabel(score); got != want {
			t.Errorf("strengthLabel(%d) = %q, want %q", score, got, want)
		}
	}
}

func TestAssessWeakAndStrong(t *testing.T) {
	weak := AssessPassphrase("password123", nil)
	if !weak.Weak || weak.Score > 1 {
		t.Fatalf("expected weak, got %+v", weak)
	}
	if weak.Entropy > 30 {
		t.Fatalf("weak entropy suspiciously high: %v", weak.Entropy)
	}
	strong := AssessPassphrase("con meo ngu trua tam biet", nil)
	if strong.Weak {
		t.Fatalf("expected strong, got %+v", strong)
	}
	if strong.Label != "mạnh" && strong.Label != "rất mạnh" {
		t.Fatalf("unexpected label %q", strong.Label)
	}
	if strong.Tokens != 6 {
		t.Fatalf("tokens = %d, want 6", strong.Tokens)
	}
}

func TestAssessEntropyCap(t *testing.T) {
	r := AssessPassphrase(strings.Repeat("kq xw ", 40), nil)
	if r.Entropy > 128.0 {
		t.Fatalf("entropy must cap at 128, got %v", r.Entropy)
	}
}

func TestAssessPersonalContext(t *testing.T) {
	plain := AssessPassphrase("elsutm-chat-2024", nil)
	withCtx := AssessPassphrase("elsutm-chat-2024", []string{"elsutm", "chat.elsutm.io.vn"})
	if !(withCtx.Score <= plain.Score && withCtx.Entropy <= plain.Entropy) {
		t.Fatalf("personal context must not raise score: %+v vs %+v", withCtx, plain)
	}
}

func TestAssessCappedFlag(t *testing.T) {
	huge := AssessPassphrase(strings.Repeat("correct horse battery staple ", 10), nil)
	if !huge.Capped || huge.Entropy != 128 {
		t.Errorf("huge passphrase = %+v, want Capped with Entropy 128", huge)
	}
	small := AssessPassphrase("abc123", nil)
	if small.Capped {
		t.Errorf("small passphrase must not be capped: %+v", small)
	}
	if got := (StrengthReport{Score: 0, Label: "yếu"}).WeakWarning(); !strings.Contains(got, "yếu") || strings.Contains(got, "128") {
		t.Fatalf("warn must not leak entropy: %q", got)
	}
}

func TestReadDoubleEntry(t *testing.T) {
	script := func(lines ...string) func() (string, error) {
		i := 0
		return func() (string, error) {
			if i >= len(lines) {
				return "", errors.New("eof")
			}
			s := lines[i]
			i++
			if s == "\x00err" {
				return "", errors.New("read fail")
			}
			return s, nil
		}
	}
	got, err := readDoubleEntry(script("same phrase here", "same phrase here"))
	if err != nil || got != "same phrase here" {
		t.Fatalf("got %q %v", got, err)
	}
	got, err = readDoubleEntry(script("one", "two", "same phrase here", "same phrase here"))
	if err != nil || got != "same phrase here" {
		t.Fatalf("mismatch must restart, got %q %v", got, err)
	}
	if _, err := readDoubleEntry(script("a", "b", "c", "d", "e", "f", "g", "h")); err == nil {
		t.Fatal("must abort after 3 mismatched rounds")
	}
	if _, err := readDoubleEntry(script("", "")); err == nil {
		t.Fatal("empty must abort")
	}
	if _, err := readDoubleEntry(script("\x00err")); err == nil {
		t.Fatal("read error must abort")
	}
}

func TestConfirmUseWeak(t *testing.T) {
	for in, want := range map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "có\n": true, "co\n": true,
		"\n": false, "n\n": false, "xyz\n": false, "": false,
	} {
		if got := confirmUseWeak(strings.NewReader(in)); got != want {
			t.Errorf("confirmUseWeak(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestUserInputs(t *testing.T) {
	if got := userInputs("  ", ""); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	got := userInputs("bob", "wss://h.example/")
	if len(got) != 2 || got[0] != "bob" {
		t.Fatalf("got %v", got)
	}
}

func TestToAssessment(t *testing.T) {
	rep := StrengthReport{Score: 1, Entropy: 12.5, Label: "yếu", Weak: true}
	got := toAssessment(rep)
	if got.Bits != 12.5 || got.Score != 1 || got.Label != "yếu" || !got.Weak || got.Capped {
		t.Errorf("toAssessment = %+v, want fields carried over, uncapped", got)
	}
	rep = AssessPassphrase("correct horse battery staple", nil)
	got = toAssessment(rep)
	if got.Bits != rep.Entropy || got.Score != rep.Score || got.Label != rep.Label || got.Weak != rep.Weak {
		t.Errorf("toAssessment(assessed) = %+v vs %+v", got, rep)
	}
}
