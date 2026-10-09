//go:build !js

package main

// Desktop passkey pairing. The client asks the server for a ceremony,
// prints the URL, opens a browser if it can, and then waits on the socket
// until the server answers. The browser may be a phone on another network,
// which is why the URL is always printed and never only opened.

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/CleveTok3125/V2V/internal/tui"
)

// pairOpener builds the command that opens a URL, without running it. The
// URL reaches the process as its own argv entry and never through a shell,
// so nothing in it can be word-split or expanded. Splitting this from
// openPairURL is what lets the choice be tested at all: running an opener
// from a test spawns a real process and a real browser.
func pairOpener(goos, url string) (string, []string, bool) {
	if url == "" {
		return "", nil, false
	}
	switch goos {
	case "darwin":
		return "open", []string{url}, true
	case "windows":
		// rundll32 is the handler every supported Windows version ships,
		// unlike the Start-Process cmdlet which is PowerShell-only.
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, true
	default:
		return "xdg-open", []string{url}, true
	}
}

// openPairURL hands a URL to the desktop's browser. It reports whether an
// opener was found; a false return is informational, never fatal, because
// the link has already been printed for this exact case.
func openPairURL(url string) bool {
	name, args, ok := pairOpener(runtime.GOOS, url)
	if !ok {
		return false
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return false
	}
	// Reap the child: it exits on its own, and the caller must not block
	// the login on a browser that took its time.
	go func() { _ = cmd.Wait() }()
	return true
}

// printPairURL shows the link and, when there is a terminal to open it from,
// also asks the desktop to open it. Printing comes first so the user always
// has the link even if the opener is missing or hangs.
//
// Without a controlling terminal there is nobody watching: the client is
// piped, backgrounded, or headless. Spawning a browser then would open a
// window on a machine with nobody in front of it.
func printPairURL(url string) {
	fmt.Println()
	fmt.Println("🔗 Mở link này trên trình duyệt để xác thực bằng passkey:")
	fmt.Println()
	fmt.Println("   " + url)
	fmt.Println()
	if !tui.HasControllingTTY() || !openPairURL(url) {
		fmt.Println("Không mở được trình duyệt tự động — hãy dán link trên.")
		return
	}
	fmt.Println("Đã thử mở trình duyệt. Nếu không có gì xảy ra, hãy dán link trên.")
}

// pairWaitPrompt is what the client shows while the browser has the floor.
// The budget comes from the server, so the number quoted here cannot drift
// from the window the ticket actually has.
func pairWaitPrompt(role string, ttlSec int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏳ Đang chờ xác thực passkey cho role [%s]...\n", role)
	fmt.Fprintf(&b, "   Mở link, xác nhận bằng passkey. Link hết hạn sau %s.\n", humanDuration(ttlSec))
	fmt.Fprintf(&b, "   Nhấn Ctrl+C để hủy.\n")
	return b.String()
}

// humanDuration renders a second count the way a person would say it, and
// falls back to seconds so a short or absent budget never reads as "0m".
func humanDuration(sec int) string {
	if sec <= 0 {
		return "ít thời gian"
	}
	if sec < 60 {
		return fmt.Sprintf("%d giây", sec)
	}
	if sec%60 == 0 {
		return fmt.Sprintf("%d phút", sec/60)
	}
	return fmt.Sprintf("%d phút %d giây", sec/60, sec%60)
}
