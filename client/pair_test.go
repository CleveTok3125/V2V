//go:build !js

package main

// The browser opener is best effort: the link is always printed, so a
// missing opener is a worse experience, never a broken login. These tests
// pin the parts a person reads.

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// TestHumanDuration reads the way a person would say it. A client that
// printed "0m" while waiting five minutes would make the wait look broken.
func TestHumanDuration(t *testing.T) {
	cases := []struct {
		sec  int
		want string
	}{
		{0, "ít thời gian"},
		{-5, "ít thời gian"},
		{1, "1 giây"},
		{45, "45 giây"},
		{60, "1 phút"},
		{300, "5 phút"},
		{330, "5 phút 30 giây"},
		{90, "1 phút 30 giây"},
	}
	for _, c := range cases {
		if got := humanDuration(c.sec); got != c.want {
			t.Errorf("humanDuration(%d) = %q, want %q", c.sec, got, c.want)
		}
	}
}

// TestPairWaitPromptNamesTheBudget: someone who closed the tab needs to know
// whether it is worth opening it again.
func TestPairWaitPromptNamesTheBudget(t *testing.T) {
	msg := pairWaitPrompt("admin", 300)
	for _, want := range []string{"admin", "5 phút", "Ctrl+C"} {
		if !strings.Contains(msg, want) {
			t.Errorf("prompt missing %q:\n%s", want, msg)
		}
	}
	// An absent budget must not read as a wait of nothing.
	if got := pairWaitPrompt("admin", 0); !strings.Contains(got, "ít thời gian") {
		t.Errorf("a zero budget printed as %q", strings.TrimSpace(got))
	}
}

// TestPrintPairURLAlwaysShowsTheLink: the browser may be on a phone, a
// locked screen, or not opened at all. The link is the only channel that
// survives all three, so it is printed before any opener is tried.
func TestPrintPairURLAlwaysShowsTheLink(t *testing.T) {
	const url = "https://chat.example.com/web/#pair=deadbeef"
	got := captureStdout(t, func() { printPairURL(url) })

	if !strings.Contains(got, url) {
		t.Errorf("the link was not printed:\n%s", got)
	}
	// Whichever opener ran or failed, the user is told what to do.
	if !strings.Contains(got, "dán link") {
		t.Errorf("no next step offered to the user:\n%s", got)
	}
}

// captureStdout runs fn with os.Stdout on a pipe and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	os.Stdout = saved
	return <-done
}

// TestPairOpenerPicksThePlatformOpener pins which opener each platform gets.
// rundll32 rather than Start-Process because the cmdlet is PowerShell-only.
func TestPairOpenerPicksThePlatformOpener(t *testing.T) {
	const url = "https://chat.example.com/web/#pair=ab"
	cases := []struct {
		goos string
		name string
		args []string
	}{
		{"darwin", "open", []string{url}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", url}},
		{"linux", "xdg-open", []string{url}},
		{"freebsd", "xdg-open", []string{url}},
	}
	for _, c := range cases {
		name, args, ok := pairOpener(c.goos, url)
		if !ok {
			t.Errorf("pairOpener(%q) refused a URL", c.goos)
			continue
		}
		if name != c.name {
			t.Errorf("pairOpener(%q) = %q, want %q", c.goos, name, c.name)
		}
		if strings.Join(args, " ") != strings.Join(c.args, " ") {
			t.Errorf("pairOpener(%q) args = %q, want %q", c.goos, args, c.args)
		}
	}
}

// TestPairOpenerKeepsTheURLAsOneArgument is the reason the opener is built
// separately from running it. The URL arrives over the socket, so it must
// reach the process as a single argument: no word splitting, no expansion,
// and no way for it to become more than one argv entry.
func TestPairOpenerKeepsTheURLAsOneArgument(t *testing.T) {
	// Every metacharacter that a shell would act on, in one URL.
	hostile := "https://chat.example.com/web/#pair=$(id);rm -rf / `whoami` \"x\""
	for _, goos := range []string{"darwin", "windows", "linux"} {
		_, args, ok := pairOpener(goos, hostile)
		if !ok {
			t.Fatalf("pairOpener(%q) refused the URL", goos)
		}
		if got := args[len(args)-1]; got != hostile {
			t.Errorf("pairOpener(%q): URL was rewritten\n got %q\nwant %q", goos, got, hostile)
		}
		// Nothing was split off it: the URL is still exactly one element.
		total := 0
		for _, a := range args {
			total += len(a)
		}
		fixed := 0
		switch goos {
		case "windows":
			fixed = len("url.dll,FileProtocolHandler")
		default:
			fixed = 0
		}
		if total != len(hostile)+fixed {
			t.Errorf("pairOpener(%q): args = %q, the URL was split or padded", goos, args)
		}
	}
}

// TestPairOpenerRefusesAnEmptyURL: an empty link is not a link, and opening
// one would hand the opener a bare program name.
func TestPairOpenerRefusesAnEmptyURL(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		if _, _, ok := pairOpener(goos, ""); ok {
			t.Errorf("pairOpener(%q, \"\") built a command", goos)
		}
	}
}
