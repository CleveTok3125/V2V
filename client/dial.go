package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Connection setup, extracted from main: dial with TLS auto-upgrade,
// then the auth-challenge read. Pure session bootstrap; the chat loop
// never re-enters here.

// dialWithUpgrade dials wsURL, retrying once as wss:// when the server
// answers 426 Upgrade Required. It returns the live connection, the
// effective URL (upgraded or original), or a nil connection with the
// failure already reported.
func dialWithUpgrade(wsURL string) (wsConn, string, error) {
	conn, resp, err := dialWS(wsURL)
	if err != nil {
		// Auto-upgrade ws:// -> wss:// when server requires TLS (426)
		if resp != nil && resp.StatusCode == http.StatusUpgradeRequired && strings.HasPrefix(wsURL, "ws://") {
			wssURL := "wss://" + strings.TrimPrefix(wsURL, "ws://")
			fmt.Printf("🔒 Server yêu cầu wss://, đang thử lại với %s…\n", wssURL)
			if bodyBytes, _ := io.ReadAll(resp.Body); len(bodyBytes) > 0 {
				fmt.Printf("📦 Server: %s\n", strings.TrimSpace(string(bodyBytes)))
			}
			conn2, resp2, err2 := dialWS(wssURL)
			if err2 == nil {
				return conn2, wssURL, nil
			}
			fmt.Printf("❌ Thử lại wss cũng thất bại: %v\n", err2)
			if resp2 != nil {
				fmt.Printf("👉 HTTP Status Code: %d\n", resp2.StatusCode)
			}
			return nil, wsURL, err2
		}
		fmt.Println("❌ Không thể kết nối:", err)
		if resp != nil {
			fmt.Printf("👉 HTTP Status Code: %d\n", resp.StatusCode)
			bodyBytes, _ := io.ReadAll(resp.Body)
			if len(bodyBytes) > 0 {
				fmt.Printf("📦 Nội dung phản hồi: %s\n", strings.TrimSpace(string(bodyBytes)))
			}
		}
		return nil, wsURL, err
	}
	return conn, wsURL, nil
}

// readChallenge reads the opening auth_challenge packet. Anything else
// (or a read failure) aborts the session before any secret is derived.
func readChallenge(conn wsConn) (AuthPacket, error) {
	var challenge AuthPacket
	if err := conn.ReadJSON(&challenge); err != nil {
		return AuthPacket{}, err
	}
	if challenge.Type != "auth_challenge" {
		fmt.Println("❌ Lỗi: Server không gửi Auth Challenge hợp lệ.")
		return AuthPacket{}, errors.New("expected auth_challenge")
	}
	return challenge, nil
}
