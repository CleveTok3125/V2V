package tui

import (
	"strings"
	"testing"
)

func TestConfirmPiped(t *testing.T) {
	cases := map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "YES\n": true,
		"có\n": true, "co\n": true, "n\n": false, "\n": false,
		"no\n": false, "maybe\n": false, "": false,
	}
	for in, want := range cases {
		if got := ConfirmPiped(strings.NewReader(in)); got != want {
			t.Errorf("ConfirmPiped(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestReadLineKeepsRemainder(t *testing.T) {
	r := strings.NewReader("first\nsecond\n")
	first, err := ReadLine(r)
	if err != nil || first != "first" {
		t.Fatalf("first = %q, %v", first, err)
	}
	rest, err := ReadLine(r)
	if err != nil || rest != "second" {
		t.Errorf("no read-ahead: rest = %q, %v", rest, err)
	}
}
