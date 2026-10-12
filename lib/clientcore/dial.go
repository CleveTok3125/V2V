package clientcore

// Connection setup: dial with TLS auto-upgrade, solve the join gate when the
// server demands one, then read the auth challenge. Pure session bootstrap;
// the chat loop never re-enters here.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/CleveTok3125/V2V/lib/filter"
	"github.com/CleveTok3125/V2V/lib/identity"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// dialErrorBody is the most of a failed dial's response body the core reads
// for display: it is server-supplied, so the read is bounded.
const dialErrorBody = 256 << 10

// defaultGateAttempts is used when DialConfig.MaxAttempts is unset.
const defaultGateAttempts = 3

// defaultReadLimit caps one inbound frame when the caller sets no limit.
// Without it a gorilla-backed Conn reads unbounded, because SetReadLimit(0)
// means "no limit" rather than "unset".
const defaultReadLimit = 1 << 20

// ErrNotAuthChallenge reports that the server's first frame was not an
// auth_challenge. It is a protocol mismatch rather than a transport failure,
// so a caller can report it as such instead of treating it as a dead socket.
var ErrNotAuthChallenge = errors.New("expected auth_challenge")

// DialBodyText reads a bounded, sanitized slice of a failed response body.
// A nil response yields "".
func DialBodyText(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, dialErrorBody))
	return filter.SanitizeForDisplay(strings.TrimSpace(string(bodyBytes)))
}

// IdentityPin returns the pinned server pubkey from a plaintext key file, or
// "" when absent or encrypted: unlocking would prompt before the dial, so an
// encrypted pin falls back to trust-on-first-use.
func IdentityPin(keyFile string) string {
	if keyFile == "" {
		return ""
	}
	if enc, _ := identity.IsEncrypted(keyFile); enc {
		return ""
	}
	idf, err := identity.Load(keyFile)
	if err != nil || idf == nil || idf.Ed25519 == nil {
		return ""
	}
	return idf.Ed25519.ServerPubKey
}

// gateChallengeBody reads a failed dial's response body once and reports
// whether it is a join-gate challenge. The caller reuses the returned text
// instead of re-reading the drained body.
func gateChallengeBody(resp *http.Response) (raw string, isGate bool) {
	if resp == nil || resp.Body == nil {
		return "", false
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, dialErrorBody))
	raw = filter.SanitizeForDisplay(strings.TrimSpace(string(bodyBytes)))
	var probe struct {
		Required bool `json:"required"`
	}
	if resp.StatusCode == http.StatusTooManyRequests && json.Unmarshal(bodyBytes, &probe) == nil {
		isGate = probe.Required
	}
	return raw, isGate
}

// DialManaged dials with TLS auto-upgrade plus join-gate retries: on a 429
// gate challenge it solves the proof of work, waits out the ticket and
// redials with the lease pass, up to the configured attempt budget.
func (d Dialer) DialManaged(wsURL string) (Conn, string, error) {
	url := wsURL
	upgraded := false
	attempts := d.Config.MaxAttempts
	if attempts < 1 {
		attempts = defaultGateAttempts
	}
	// pass is empty on native (the 429 is handled below) and carries a lease
	// pass on wasm, where the browser hides the 429.
	pass := ""
	if d.Gate != nil {
		pass = d.Gate.Pass(url)
	}
	pin := IdentityPin(d.Config.KeyFile)
	for attempt := 0; attempt < attempts; attempt++ {
		dialURL := url
		if pass != "" {
			sep := "?"
			if strings.Contains(dialURL, "?") {
				sep = "&"
			}
			dialURL += sep + "gate_pass=" + pass
		}
		conn, resp, err := d.Transport.Dial(dialURL)
		if err == nil && conn != nil {
			limit := d.Config.ReadLimit
			if limit <= 0 {
				limit = defaultReadLimit
			}
			conn.SetReadLimit(limit)
			return conn, dialURL, nil
		}
		if resp != nil && resp.StatusCode == http.StatusUpgradeRequired && strings.HasPrefix(url, "ws://") && !upgraded {
			upgraded = true
			url = "wss://" + strings.TrimPrefix(url, "ws://")
			if d.Output != nil {
				d.Output.OnDial(DialEvent{Kind: DialUpgraded, URL: url})
			}
			continue
		}
		body, isGate := gateChallengeBody(resp)
		if !isGate {
			if d.Output != nil {
				status := 0
				if resp != nil {
					status = resp.StatusCode
				}
				d.Output.OnDial(DialEvent{Kind: DialFailed, Err: err, Status: status, Body: body})
			}
			return nil, url, err
		}
		if d.Gate == nil {
			return nil, url, err
		}
		if d.Output != nil {
			d.Output.OnDial(DialEvent{Kind: DialGateRequired})
		}
		next, ok := d.Gate.Solve(url, pin)
		if !ok {
			return nil, url, err
		}
		pass = next
	}
	return nil, url, errors.New("join gate retries exhausted")
}

// ReadChallenge reads the opening auth_challenge packet. Anything else (or a
// read failure) aborts the session before any secret is derived. A frame of
// another type yields ErrNotAuthChallenge; a read failure yields the socket
// error, so a caller can tell a protocol mismatch from a dead connection.
func ReadChallenge(conn Conn) (wire.AuthPacket, error) {
	var challenge wire.AuthPacket
	if err := conn.ReadJSON(&challenge); err != nil {
		return wire.AuthPacket{}, err
	}
	if challenge.Type != "auth_challenge" {
		return wire.AuthPacket{}, ErrNotAuthChallenge
	}
	return challenge, nil
}
