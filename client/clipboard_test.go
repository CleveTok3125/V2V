package main

import (
	"errors"
	"testing"
)

func TestShouldClear(t *testing.T) {
	if !shouldClear("secret", "secret", nil) {
		t.Fatal("identical content must clear")
	}
	if shouldClear("user stuff", "secret", nil) {
		t.Fatal("newer user content must never clear")
	}
	if shouldClear("", "secret", nil) {
		t.Fatal("already-empty clipboard must not rewrite")
	}
	if shouldClear("secret", "secret", errors.New("no display")) {
		t.Fatal("read errors must not clear")
	}
}
