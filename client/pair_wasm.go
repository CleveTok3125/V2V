//go:build js

package main

// The wasm client never pairs: its page already runs the ceremony against
// the same handshake nonce, through the 🔑 Passkey button. Asking a browser
// to sign a pair ticket would mean leaving the page that owns the session.

import "fmt"

func openPairURL(string) bool { return false }

func printPairURL(url string) {
	fmt.Println("❌ --passkey chỉ dùng cho client desktop. Trên web, dùng nút 🔑 Passkey.")
	fmt.Println("   Link này không áp dụng: " + url)
}

// pollPairStatus has nothing to do on wasm: the page runs the ceremony
// against the same handshake, so there is no out-of-band wait to poll for.
func pollPairStatus(string, string, int) bool { return false }

func pairWaitPrompt(role string, _ int) string {
	return fmt.Sprintf("⏳ Đang chờ xác thực passkey cho role [%s]...\n", role)
}

// pairBlockMarks has nothing to bracket on wasm: the client never prints a
// link of its own there, so there is no block to wipe.
func pairBlockMarks(bool) (string, string) { return "", "" }

// termHeight is 0 on wasm: there is no terminal to measure, which is also
// what keeps the pairing block marks empty above.
func termHeight() int { return 0 }
