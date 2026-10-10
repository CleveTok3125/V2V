//go:build !js

package main

// The browser opener is best effort: the link is always printed, so a
// missing opener is a worse experience, never a broken login. These tests
// pin the parts a person reads.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"rsc.io/qr"
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
	got := captureStdout(t, func() { fmt.Println(pairLinkText(url)) })

	if !strings.Contains(got, url) {
		t.Errorf("the link was not printed:\n%s", got)
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

// TestRenderPairQRFitsOrRefuses: a code that scrolls or wraps cannot be
// scanned, so the renderer refuses rather than emitting something unusable.
// The level and the size it produces are both asserted, not derived: the
// level trades scan tolerance against rows, so moving it changes whether the
// code fits a real window and has to be a deliberate act rather than a
// silent one.
func TestRenderPairQRFitsOrRefuses(t *testing.T) {
	if qrLevel != qr.M {
		t.Fatal("error-correction level changed: the size below and the threshold in the docs assume M")
	}
	url := "https://chat.example.com/web/#pair=" + strings.Repeat("ab", 32)
	const wantRows, wantCols = 25, 49

	drawn, ok := renderPairQR(url, wantRows, wantCols)
	if !ok {
		t.Fatalf("refused a window of exactly %dx%d", wantRows, wantCols)
	}
	lines := strings.Split(strings.TrimRight(drawn, "\n"), "\n")
	if len(lines) != wantRows {
		t.Errorf("drew %d rows, want %d", len(lines), wantRows)
	}
	for i, line := range lines {
		if got := len([]rune(qrStripColour(line))); got != wantCols {
			t.Fatalf("row %d is %d columns, want %d", i, got, wantCols)
		}
	}
	if _, ok := renderPairQR(url, wantRows-1, wantCols); ok {
		t.Error("a window one row short still got a code")
	}
	if _, ok := renderPairQR(url, wantRows, wantCols-1); ok {
		t.Error("a window one column short got a code, which the terminal would wrap")
	}
}

// TestRenderPairQRMatchesTheEncoder: the drawing is ours, so a swapped half
// or a shifted margin would produce a code that still looks like a QR and
// still refuses to scan. Rebuilding the module grid from the glyphs and
// comparing it with the encoder is what proves the picture is the symbol.
func TestRenderPairQRMatchesTheEncoder(t *testing.T) {
	url := "https://chat.example.com/web/#pair=abcd"
	drawn, ok := renderPairQR(url, 200, 200)
	if !ok {
		t.Fatal("refused with a huge room")
	}
	want, err := qr.Encode(url, qrLevel)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(drawn, "\n"), "\n")

	// The margin is checked against the picture with the width the standard
	// requires, not with the drawing's own constant: a scanner finds the
	// symbol's edge in that margin, so shrinking it has to fail here.
	const specQuiet = 4
	for row, line := range lines {
		mods := qrLineModules(t, line)
		if len(mods) < specQuiet*2 {
			t.Fatalf("row %d is only %d cells wide", row, len(mods))
		}
		for col, cell := range mods {
			if col >= specQuiet && col < len(mods)-specQuiet {
				continue
			}
			if cell[0] || cell[1] {
				t.Fatalf("row %d column %d is not background", row, col)
			}
		}
	}

	// And the symbol itself is the encoder's, module for module.
	for row, line := range lines {
		for col, cell := range qrLineModules(t, line) {
			if got := qrModule(want, col, row*2); got != cell[0] {
				t.Fatalf("module (%d,%d): drawn %v, encoder says %v", col, row*2, cell[0], got)
			}
			if got := qrModule(want, col, row*2+1); got != cell[1] {
				t.Fatalf("module (%d,%d): drawn %v, encoder says %v", col, row*2+1, cell[1], got)
			}
		}
	}
}

// TestRenderPairQRCarriesItsOwnColours: the cell colours must be explicit.
// Half-block cells take their top from the foreground and their bottom from
// the background, so relying on the terminal's palette draws a light-on-dark
// code on a dark theme, which some scanners refuse.
func TestRenderPairQRCarriesItsOwnColours(t *testing.T) {
	code, ok := renderPairQR("https://chat.example.com/web/#pair="+strings.Repeat("cd", 32), 60, 80)
	if !ok {
		t.Fatal("refused")
	}
	for _, want := range []string{"\x1b[30;40m", "\x1b[30;47m", "\x1b[37;47m", "\x1b[37;40m"} {
		if !strings.Contains(code, want) {
			t.Errorf("missing cell colour %q", want)
		}
	}
}

// TestRenderPairQRRefusesNoRoom: a window with no rows at all is not an
// invitation to draw.
func TestRenderPairQRRefusesNoRoom(t *testing.T) {
	if code, ok := renderPairQR("https://chat.example.com/web/#pair=x", 0, 0); ok || code != "" {
		t.Fatal("a zero-row window got a code")
	}
}

// TestHasGraphicalSession: the browser opener runs on the machine the client
// runs on, so a Linux box with no display has nothing to open into and must
// not be told otherwise. macOS and Windows always have a session.
func TestHasGraphicalSession(t *testing.T) {
	cases := []struct {
		goos, display, wayland string
		want                   bool
	}{
		{"linux", "", "", false},
		{"linux", ":0", "", true},
		{"linux", "", "wayland-0", true},
		{"linux", ":0", "wayland-0", true},
		{"freebsd", "", "", false},
		{"darwin", "", "", true},
		{"windows", "", "", true},
	}
	for _, c := range cases {
		if got := hasGraphicalSession(c.goos, c.display, c.wayland); got != c.want {
			t.Errorf("hasGraphicalSession(%q,%q,%q) = %v, want %v",
				c.goos, c.display, c.wayland, got, c.want)
		}
	}
}

// qrStripColour replaces each cell with its glyph, so a line can be measured
// in columns rather than in bytes.
func qrStripColour(line string) string {
	for _, cell := range []string{qrBothDark, qrTopDark, qrBothLight, qrTopLight} {
		line = strings.ReplaceAll(line, cell, "▀")
	}
	return line
}

// qrReadCell is the inverse of the drawing: it reports the two modules a cell
// stands for, and the cell text itself so a walk can advance.
func qrReadCell(s string) (topDark, bottomDark bool, cell string) {
	switch {
	case strings.HasPrefix(s, qrBothDark):
		return true, true, qrBothDark
	case strings.HasPrefix(s, qrTopDark):
		return true, false, qrTopDark
	case strings.HasPrefix(s, qrBothLight):
		return false, false, qrBothLight
	case strings.HasPrefix(s, qrTopLight):
		return false, true, qrTopLight
	}
	return false, false, ""
}

// qrLineModules parses one drawn line into the module pairs its cells encode.
func qrLineModules(t *testing.T, line string) [][2]bool {
	t.Helper()
	var out [][2]bool
	for rest := line; rest != ""; {
		top, bottom, cell := qrReadCell(rest)
		if cell == "" {
			t.Fatalf("unreadable cell in %q", rest)
		}
		out = append(out, [2]bool{top, bottom})
		rest = rest[len(cell):]
	}
	return out
}

// TestPairBlockMarksOnlyWithATerminal pins the escapes that let the pairing
// link and its code be wiped once a browser has proved the ticket. Without a
// terminal the marks must be empty: the sequence would otherwise land in
// whatever the output is piped or redirected to.
func TestPairBlockMarksOnlyWithATerminal(t *testing.T) {
	begin, end := pairBlockMarks(false)
	if begin != "" || end != "" {
		t.Fatalf("no terminal: begin=%q end=%q, want both empty", begin, end)
	}

	begin, end = pairBlockMarks(true)
	if begin == "" || end == "" {
		t.Fatal("a terminal must get both marks")
	}
	// Save the position before the block and restore it before erasing:
	// counting lines instead would be wrong as soon as the link wraps.
	if !strings.HasPrefix(begin, "\x1b7") {
		t.Errorf("begin = %q, want the cursor saved first", begin)
	}
	if !strings.HasPrefix(end, "\x1b8") {
		t.Errorf("end = %q, want the cursor restored first", end)
	}
	if !strings.Contains(end, "\x1b[J") {
		t.Errorf("end = %q, want an erase from the restored position", end)
	}
	// The restore must come before the erase, or the wrong region goes.
	if strings.Index(end, "\x1b8") > strings.Index(end, "\x1b[J") {
		t.Errorf("end = %q erases before restoring", end)
	}
}
