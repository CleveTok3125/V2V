package main

// The desktop half of passkey pairing. The browser ceremony can take
// minutes, so the wait does not happen on a chat socket: the ticket is
// fetched on a throwaway connection, the link is shown, and the result is
// collected by polling until a browser has proved the ticket. Only then
// does the real connection open.
//
// See docs/TECHNICAL.md, "Desktop pairing".

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/CleveTok3125/V2V/internal/filter"
)

// validateAuthFlags refuses a login that names two credentials. -P owns the
// whole pre-auth exchange and -K owns the packet body; sending both would
// leave the server to guess which one decided the role.
func validateAuthFlags() error {
	if CLI.Passkey != "" && CLI.KeyFile != "" {
		return fmt.Errorf("--passkey và --key-file không dùng cùng nhau")
	}
	return nil
}

// validateDisplayName refuses a display name the server would silently
// replace. The client sends what the user typed, and the server turns an
// invalid name into "Anonymous" without saying so, so the same rule runs here
// where the reason can reach the user. An empty name is not refused: the
// server reads it as "Anonymous", which is what the panel placeholder already
// promises.
func validateDisplayName(name string) error {
	if name == "" {
		return nil
	}
	if err := filter.ValidateDisplayName(name); err != nil {
		return fmt.Errorf("Tên hiển thị không hợp lệ (%v)", err)
	}
	return nil
}

// readAuthResult reads the one frame that ends a handshake. The pairing
// wait no longer happens on this connection, so anything but a verdict
// means the socket is not talking to the server it thinks it is.
func readAuthResult(s *Session) (AuthPacket, error) {
	var packet AuthPacket
	if err := s.Conn.ReadJSON(&packet); err != nil {
		fmt.Println("❌ Lỗi đọc phản hồi xác thực:", err)
		return packet, err
	}
	return packet, nil
}

// serverPasskeyEnabled asks whether the server can run a browser ceremony
// at all. known=false means the answer could not be read — an old server
// without the endpoint, a proxy, a truncated body — and the caller must
// carry on: the handshake is what actually decides, and refusing here would
// lock out servers that are merely older than this client.
func serverPasskeyEnabled(httpBase string) (enabled, known bool) {
	client, err := serverHTTPClient()
	if err != nil {
		return false, false
	}
	resp, err := client.Get(httpBase + "/api/capabilities")
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	if err != nil {
		return false, false
	}
	var v struct {
		Passkey *bool `json:"passkey"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Passkey == nil {
		return false, false
	}
	return *v.Passkey, true
}

// pairRequest is what the desktop posts to ask for a ceremony. The role
// names which credentials the ceremony may prove; the server confirms it
// exists and never lets the page choose it.
type pairRequest struct {
	Role string `json:"role"`
}

// pairOffer is the server's answer: the ticket, and the link that proves it.
type pairOffer struct {
	Ticket string `json:"ticket"`
	URL    string `json:"url"`
	TTLSec int    `json:"ttl_sec"`
}

// acquirePairTicket fetches a ticket, shows the link, and waits for a
// browser to prove it. ok=false means the caller must give up: every
// failure here has already been explained to the user.
//
// The ask is an HTTP request, not a throwaway dial, so the connection this
// login eventually makes is the only one the server sees. A second dial
// would be refused as churn by CONNECTION_COOLDOWN.
func acquirePairTicket(wsURL, role string) (string, bool) {
	base, err := httpBaseFromWS(wsURL)
	if err != nil {
		fmt.Println("❌ Không suy ra được địa chỉ HTTP của server:", err)
		return "", false
	}
	// Asking first avoids sending someone to a browser for a link that
	// cannot work. An unreadable answer is not a refusal.
	if enabled, known := serverPasskeyEnabled(base); known && !enabled {
		fmt.Println("❌ Server này không bật passkey (thiếu WEBAUTHN_RPID/WEBAUTHN_ORIGIN).")
		return "", false
	}

	offer, ok := requestPairTicket(base, role)
	if !ok {
		return "", false
	}
	begin, end := pairBlockMarks(termHeight() > 0)
	fmt.Print(begin)
	printPairURL(offer.URL)
	fmt.Print(pairWaitPrompt(role, offer.TTLSec))

	// The ticket is in hand, so nothing is held while the ceremony runs.
	if !pollPairStatus(base, offer.Ticket, offer.TTLSec) {
		// The link stays on screen: an expired or abandoned ceremony can be
		// started again with it, and the code is how it gets to a phone.
		return "", false
	}
	fmt.Print(end)
	fmt.Println("✅ Trình duyệt đã xác thực — đang hoàn tất đăng nhập...")
	return offer.Ticket, true
}

// requestPairTicket posts the ask and decodes the offer. The server's own
// reasons are shown as they arrive; anything unreadable is reported as a
// failure rather than guessed at.
func requestPairTicket(base, role string) (pairOffer, bool) {
	var offer pairOffer
	body, err := json.Marshal(pairRequest{Role: role})
	if err != nil {
		fmt.Println("❌ Lỗi tạo yêu cầu ghép cặp:", err)
		return offer, false
	}
	client, err := serverHTTPClient()
	if err != nil {
		fmt.Println("❌ Không tạo được kết nối tới server:", err)
		return offer, false
	}
	resp, err := client.Post(base+"/pair/request", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Println("❌ Không gửi được yêu cầu ghép cặp:", err)
		return offer, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		fmt.Printf("❌ Server từ chối yêu cầu ghép cặp (HTTP %d): %s\n",
			resp.StatusCode, dialBodyText(resp))
		return offer, false
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&offer); err != nil {
		fmt.Println("❌ Phản hồi ghép cặp không đọc được:", err)
		return offer, false
	}
	if offer.Ticket == "" || offer.URL == "" {
		fmt.Println("❌ Phản hồi ghép cặp không hợp lệ.")
		return offer, false
	}
	return offer, true
}
