package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/CleveTok3125/V2V/internal/identity"
)

// Connection setup, extracted from main: dial with TLS auto-upgrade,
// then the auth-challenge read. Pure session bootstrap; the chat loop
// never re-enters here.

// dialErrorBody is the most of a failed dial's response body the client
// reads for display: it is server-supplied, so the read is bounded.
const dialErrorBody = 256 << 10

// dialBodyText reads a bounded, sanitized slice of a failed response
// body. Nil body yields "".
func dialBodyText(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, dialErrorBody))
	return serverText(strings.TrimSpace(string(bodyBytes)))
}

// maxGateAttempts bounds join-gate retries: one initial dial plus
// gate passes. A persistent 429 means the gate itself is failing, not
// a solvable challenge.
const maxGateAttempts = 3

// identityPin returns the pinned server pubkey from a plaintext key
// file, or "" when absent/encrypted: unlocking would prompt before the
// dial, so encrypted pins fall back to trust-on-first-use.
func identityPin() string {
	if CLI.KeyFile == "" {
		return ""
	}
	if enc, _ := identity.IsEncrypted(CLI.KeyFile); enc {
		return ""
	}
	idf, err := LoadIdentityFile(CLI.KeyFile)
	if err != nil || idf == nil || idf.Ed25519 == nil {
		return ""
	}
	return idf.Ed25519.ServerPubKey
}

// gateChallengeBody reads a failed dial's response body once and
// reports whether it is a join-gate challenge. The caller reuses the
// returned text instead of re-reading the drained body.
func gateChallengeBody(resp *http.Response) (raw string, isGate bool) {
	if resp == nil || resp.Body == nil {
		return "", false
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, dialErrorBody))
	raw = serverText(strings.TrimSpace(string(bodyBytes)))
	var probe struct {
		Required bool `json:"required"`
	}
	if resp.StatusCode == http.StatusTooManyRequests && json.Unmarshal(bodyBytes, &probe) == nil {
		isGate = probe.Required
	}
	return raw, isGate
}

// dialManaged dials with TLS auto-upgrade plus join-gate retries: on a
// 429 gate challenge it solves PoW, waits out the ticket and redials
// with the lease pass, up to maxGateAttempts total dials.
func dialManaged(wsURL string) (wsConn, string, error) {
	url := wsURL
	upgraded := false
	// preDialPass is empty on native (429 handled below) and fetches a
	// lease pass on wasm, where the browser hides the 429.
	pass := preDialPass(url)
	pin := identityPin()
	for attempt := 0; attempt < maxGateAttempts; attempt++ {
		dialURL := url
		if pass != "" {
			sep := "?"
			if strings.Contains(dialURL, "?") {
				sep = "&"
			}
			dialURL += sep + "gate_pass=" + pass
		}
		conn, resp, err := dialWS(dialURL)
		if err == nil && conn != nil {
			conn.SetReadLimit(clientReadLimit())
			return conn, dialURL, nil
		}
		if resp != nil && resp.StatusCode == http.StatusUpgradeRequired && strings.HasPrefix(url, "ws://") && !upgraded {
			upgraded = true
			url = "wss://" + strings.TrimPrefix(url, "ws://")
			fmt.Printf("🔒 Server yêu cầu wss://, đang thử lại với %s…\n", url)
			continue
		}
		body, isGate := gateChallengeBody(resp)
		if !isGate {
			fmt.Println("❌ Không thể kết nối:", err)
			if resp != nil {
				fmt.Printf("👉 HTTP Status Code: %d\n", resp.StatusCode)
				if body != "" {
					fmt.Printf("📦 Nội dung phản hồi: %s\n", body)
				}
			}
			return nil, url, err
		}
		fmt.Println("🧩 Server yêu cầu vượt cửa PoW, đang giải…")
		next, ok := runGateFlow(gateHTTPBase(url), pin)
		if !ok {
			return nil, url, err
		}
		pass = next
	}
	return nil, url, fmt.Errorf("join gate retries exhausted")
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
