package main

// Reading the pre-auth answer. The exchange has three steps: the client
// answers the challenge, the server may hand out a pairing ticket and then
// wait for a browser, and finally one of auth_success / auth_failed ends
// it. Only the pairing path has a middle step, so the loop is short but it
// has to exist: reading exactly one frame would treat the ticket as a
// verdict.

import (
	"errors"
	"fmt"

	"github.com/CleveTok3125/V2V/internal/wire"
)

// validateAuthFlags refuses a login that names two credentials. -P owns the
// whole pre-auth exchange and -K owns the packet body; sending both would
// leave the server to guess which one decided the role.
func validateAuthFlags() error {
	if CLI.Passkey != "" && CLI.KeyFile != "" {
		return errors.New("--passkey và --key-file không dùng cùng nhau")
	}
	return nil
}

// pairTicketReplySeen bounds how many pairing replies one handshake may
// print. The server issues at most one ticket per handshake, so a second
// one means something other than this server is on the connection.
const maxPairTicketReplies = 1

func readAuthResult(s *Session, sent *AuthPacket) (AuthPacket, error) {
	tickets := 0
	for {
		var packet AuthPacket
		if err := s.Conn.ReadJSON(&packet); err != nil {
			fmt.Println("❌ Lỗi đọc phản hồi xác thực:", err)
			return packet, err
		}
		if packet.Type == wire.AuthPair {
			tickets++
			if tickets > maxPairTicketReplies {
				err := fmt.Errorf("nhiều hơn một pairing ticket trong cùng handshake")
				fmt.Println("❌ " + err.Error())
				return packet, err
			}
			if packet.PairURL == "" {
				err := fmt.Errorf("server trả về pairing ticket nhưng không có URL")
				fmt.Println("❌ " + err.Error())
				return packet, err
			}
			printPairURL(packet.PairURL)
			fmt.Print(pairWaitPrompt(sent.Role, packet.PairTTLSec))
			continue
		}
		return packet, nil
	}
}
