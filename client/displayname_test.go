package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// The rule is the server's, applied before the name is sent: a name the
// server would quietly replace with "Anonymous" is refused here instead, so
// the user is told which character is not allowed.
func TestValidateDisplayName(t *testing.T) {
	if err := validateDisplayName(""); err != nil {
		t.Errorf("an empty name is the server's to read as Anonymous: %v", err)
	}
	for _, name := range []string{"Alice", "Nguyễn Văn A", "user_1", "a-b.c'd"} {
		if err := validateDisplayName(name); err != nil {
			t.Errorf("%q should be accepted: %v", name, err)
		}
	}
	for _, name := range []string{"x | #99:ffff", "a#1", "a:b", "✍", "___"} {
		if err := validateDisplayName(name); err == nil {
			t.Errorf("%q should be refused before it reaches the server", name)
		}
	}
}

// connect refuses an invalid name before it dials, so the user is told
// instead of chatting as "Anonymous". The port has nothing behind it: the
// point is that the refusal happens before any network step, and the message
// is what proves the name, not the unreachable host, ended the attempt.
func TestConnectRefusesAnInvalidDisplayName(t *testing.T) {
	oldServer, oldUser := CLI.Server, CLI.Username
	t.Cleanup(func() { CLI.Server, CLI.Username = oldServer, oldUser })

	CLI.Server = "ws://127.0.0.1:1/"
	CLI.Username = "x | #99:ffff"

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	refused := !NewSession().connect()
	w.Close()
	os.Stdout = oldStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !refused {
		t.Fatal("connect accepted a name the server would replace")
	}
	if !strings.Contains(string(out), "Tên hiển thị không hợp lệ") {
		t.Fatalf("the refusal did not name the problem: %q", out)
	}
}
