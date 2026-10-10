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
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/tui"

	"rsc.io/qr"

	xterm "github.com/charmbracelet/x/term"
)

// termHeight is the terminal's row count, or 0 when there is no terminal to
// measure — piped, redirected, or a size the terminal will not report.
func termHeight() int {
	if _, h, err := xterm.GetSize(os.Stdout.Fd()); err == nil && h > 0 {
		return h
	}
	return 0
}

// pairPollInterval is how long the desktop waits between asks. Polling
// short keeps the server free of anything held for the ceremony's
// duration; the interval is the only latency a person notices, and after
// confirming a passkey two seconds is not noticeable.
const pairPollInterval = 2 * time.Second

// pollPairStatus asks whether a browser has proved the ticket yet. It
// returns false once the link is gone or the budget runs out. Failures are
// explained here; success is left to the caller, which has to wipe the link
// before it can say so.
func pollPairStatus(httpBase, ticket string, ttlSec int) bool {
	budget := time.Duration(ttlSec) * time.Second
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	deadline := time.Now().Add(budget)
	// The same client the rest of the flow uses: a bare http.Client would
	// fall back to the environment proxy variables, which the session's
	// --proxy never reaches, so the poll would connect from the real address.
	client, err := serverHTTPClient()
	if err != nil {
		fmt.Println("❌ Không tạo được kết nối tới server:", err)
		return false
	}
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

// The cells draw two module rows each: the top half takes the foreground
// colour and the bottom half the background, so both are set per cell.
// Relying on the terminal's own palette would draw a light-on-dark code on a
// dark theme, and some scanners refuse an inverted code.
const (
	qrBothDark  = "\x1b[30;40m▀\x1b[0m" // top dark, bottom dark
	qrTopDark   = "\x1b[30;47m▀\x1b[0m" // top dark, bottom light
	qrBothLight = "\x1b[37;47m▀\x1b[0m" // top light, bottom light
	qrTopLight  = "\x1b[37;40m▀\x1b[0m" // top light, bottom dark
)

// qrQuiet is the margin the code needs on every side. Without it a scanner
// has no way to find the symbol's edge.
const qrQuiet = 4

// qrLevel is the error-correction level. M over L: a code read off a screen
// has to survive glare and the screen's own pixel grid, neither of which
// affects a printed one, and the two rows it costs are affordable where the
// code is shown at all.
const qrLevel = qr.M

// qrCellText picks the cell for two stacked modules. The drawing is ours
// because a terminal code has to own its quiet zone: the cells carry explicit
// colours, and a library that draws that margin as light-on-dark halves puts
// a black band under the code.
func qrCellText(topDark, bottomDark bool) string {
	switch {
	case topDark && bottomDark:
		return qrBothDark
	case topDark:
		return qrTopDark
	case bottomDark:
		return qrTopLight
	default:
		return qrBothLight
	}
}

// qrModule reports whether the module at (x, y) is dark. Anything inside the
// quiet zone, or past the symbol, is background.
func qrModule(code *qr.Code, x, y int) bool {
	x -= qrQuiet
	y -= qrQuiet
	if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
		return false
	}
	return code.Black(x, y)
}

// renderPairQR draws the link as a code for a phone to scan, and reports
// whether it fits the room given. Both dimensions matter: a code wider than
// the window wraps, and a wrapped code cannot be scanned any more than one
// that has to scroll. The encoding is rsc.io/qr's; only the drawing is here.
func renderPairQR(url string, maxRows, maxCols int) (string, bool) {
	if maxRows < 1 || maxCols < 1 {
		return "", false
	}
	code, err := qr.Encode(url, qrLevel)
	if err != nil {
		return "", false
	}
	span := code.Size + qrQuiet*2
	rows := (span + 1) / 2
	if rows > maxRows || span > maxCols {
		return "", false
	}
	var b strings.Builder
	for y := 0; y < span; y += 2 {
		for x := 0; x < span; x++ {
			b.WriteString(qrCellText(qrModule(code, x, y), qrModule(code, x, y+1)))
		}
		b.WriteByte('\n')
	}
	return b.String(), true
}

// hasGraphicalSession reports whether this machine has anywhere to open a
// browser. The opener runs where the client runs, so over SSH this is the
// remote host's display, not the screen the person is looking at.
func hasGraphicalSession(goos, display, wayland string) bool {
	if goos == "darwin" || goos == "windows" {
		return true
	}
	return display != "" || wayland != ""
}

// pairBlockMarks brackets the pairing block so what it printed can be wiped
// once a browser has proved the ticket: the link is long, the code is tall,
// and neither is worth reading again once the login is done.
//
// The cursor is saved and restored rather than counted over, so a link that
// wraps costs nothing to get right. A terminal that ignores the restore
// leaves the erase where the block ended, which is blank space below it.
func pairBlockMarks(hasTerminal bool) (begin, end string) {
	if !hasTerminal {
		return "", ""
	}
	return "\x1b7", "\x1b8\x1b[J"
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
	printPairQR(url)
	if !hasGraphicalSession(runtime.GOOS, os.Getenv("DISPLAY"), os.Getenv("WAYLAND_DISPLAY")) {
		fmt.Println("🖥️ Máy này không có màn hình đồ họa — hãy quét mã bằng điện thoại, hoặc dán link trên.")
		return
	}
	if !tui.HasControllingTTY() || !openPairURL(url) {
		fmt.Println("Không mở được trình duyệt tự động — hãy quét mã bằng điện thoại, hoặc dán link trên.")
		return
	}
	fmt.Println("Đã thử mở trình duyệt. Nếu không có gì xảy ra, hãy quét mã bằng điện thoại hoặc dán link trên.")
}

// printPairQR adds the code when the window can hold it and says why when it
// cannot, so a person on a short terminal knows the option exists.
func printPairQR(url string) {
	rows := termHeight()
	if rows <= 0 {
		return // no terminal to measure: piped, redirected, or unknown size
	}
	// Room for the link above and the hint below.
	const reservedRows = 6
	code, ok := renderPairQR(url, rows-reservedRows, termWidth())
	if !ok {
		fmt.Println("💡 Cửa sổ không đủ chỗ để hiện mã QR — phóng to cửa sổ rồi chạy lại, hoặc dán link trên.")
		return
	}
	fmt.Print(code)
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
