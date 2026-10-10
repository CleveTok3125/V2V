package main

// The desktop half of passkey pairing. The browser ceremony can take
// minutes, so the wait does not happen on a chat socket: the ticket is
// fetched on a throwaway connection, the link is shown, and the result is
// collected by polling until a browser has proved the ticket. Only then
// does the real connection open.
//
// See docs/TECHNICAL.md, "Desktop pairing".

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/CleveTok3125/V2V/internal/wire"
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
	client, err := versionHTTPClient()
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

// acquirePairTicket fetches a ticket, shows the link, and waits for a
// browser to prove it. ok=false means the caller must give up: every
// failure here has already been explained to the user.
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

	conn, _, err := dialManaged(wsURL)
	if err != nil {
		return "", false
	}
	defer conn.Close()

	challenge, err := readChallenge(conn)
	if err != nil {
		return "", false
	}
	if err := conn.WriteJSON(AuthPacket{
		Type:     wire.AuthPairRequest,
		Nonce:    challenge.Nonce,
		Role:     role,
		Username: strings.TrimSpace(CLI.Username),
		Platform: clientPlatform(),
	}); err != nil {
		fmt.Println("❌ Lỗi gửi yêu cầu ghép cặp:", err)
		return "", false
	}

	var reply AuthPacket
	if err := conn.ReadJSON(&reply); err != nil {
		fmt.Println("❌ Không nhận được vé ghép cặp:", err)
		return "", false
	}
	if reply.Type != wire.AuthPair || reply.PairTicket == "" || reply.PairURL == "" {
		fmt.Println("❌ Phản hồi ghép cặp không hợp lệ.")
		return "", false
	}

	printPairURL(reply.PairURL)
	fmt.Print(pairWaitPrompt(role, reply.PairTTLSec))
	// The ticket is in hand, so this socket has done its job. Holding it
	// would cost a connection slot for the whole ceremony and an idle path
	// could cut it before the browser ever speaks.
	conn.Close()

	if !pollPairStatus(base, reply.PairTicket, reply.PairTTLSec) {
		return "", false
	}
	return reply.PairTicket, true
}
