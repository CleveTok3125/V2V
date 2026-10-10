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
