//go:build !js

package main

// Desktop passkey pairing. The client asks the server for a ceremony,
// prints the URL, opens a browser if it can, and then waits on the socket
// until the server answers. The browser may be a phone on another network,
// which is why the URL is always printed and never only opened.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/tui"
)

// pairPollInterval is how long the desktop waits between asks. Polling
// short keeps the server free of anything held for the ceremony's
// duration; the interval is the only latency a person notices, and after
// confirming a passkey two seconds is not noticeable.
const pairPollInterval = 2 * time.Second

// pollPairStatus asks whether a browser has proved the ticket yet. It
// returns false once the link is gone or the budget runs out, and every
// outcome is explained here so the caller has nothing to add.
func pollPairStatus(httpBase, ticket string, ttlSec int) bool {
	budget := time.Duration(ttlSec) * time.Second
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	deadline := time.Now().Add(budget)
	client := &http.Client{Timeout: 10 * time.Second}
	statusURL := httpBase + "/pair/status?ticket=" + url.QueryEscape(ticket)

	// Ask before waiting: a browser on another device may already have
	// finished, and there is no reason to sit out an interval first.
	for time.Now().Before(deadline) {
		resp, err := client.Get(statusURL)
		if err != nil {
			// A dropped poll is not a failed ceremony: the next one may
			// still arrive, and only the budget running out ends the wait.
			continue
		}
		switch resp.StatusCode {
		case http.StatusOK:
			var body struct {
				State string `json:"state"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
			resp.Body.Close()
			if body.State == "verified" {
				fmt.Println("✅ Trình duyệt đã xác thực — đang hoàn tất đăng nhập...")
				return true
			}
		case http.StatusGone:
			resp.Body.Close()
			fmt.Println("❌ Link ghép cặp đã hết hạn hoặc không còn hiệu lực.")
			return false
		default:
			// Meant for a later poll; say nothing and keep asking.
			resp.Body.Close()
		}
		time.Sleep(pairPollInterval)
	}
	fmt.Println("❌ Hết thời gian chờ xác thực trên trình duyệt.")
	return false
}

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
	fmt.Println(pairLinkText(url))
	if !tui.HasControllingTTY() || !openPairURL(url) {
		fmt.Println("Không mở được trình duyệt tự động — hãy dán link trên.")
		return
	}
	fmt.Println("Đã thử mở trình duyệt. Nếu không có gì xảy ra, hãy dán link trên.")
}

// pairLinkText is everything the person needs to read: the link, and why
// they are looking at it. It is separate from printing so the message can
// be checked without starting a browser.
func pairLinkText(url string) string {
	return "\n🔗 Mở link này trên trình duyệt để xác thực bằng passkey:\n\n   " + url + "\n"
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
