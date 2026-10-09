//go:build !js

package main

// The pre-auth read loop. It has to tolerate the server's middle step
// (a pairing ticket, which is not a verdict) and refuse anything else that
// is not a verdict, because every one of those frames opens a UI that would
// then fail on its first read.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/wire"
	"github.com/gorilla/websocket"
)

// scriptedSocket hands the client a fixed sequence of frames, then holds the
// connection open so a client that stops reading fails rather than seeing EOF.
func scriptedSocket(t *testing.T, frames ...AuthPacket) *Session {
	t.Helper()
	up := websocket.Upgrader{}
	ready := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ready <- conn
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	server := <-ready
	t.Cleanup(func() { server.Close() })
	go func() {
		for _, f := range frames {
			if err := server.WriteJSON(f); err != nil {
				return
			}
		}
		// Hold the connection open: a client that stops reading must fail
		// on its own logic, not on an EOF from here.
		for {
			if _, _, err := server.ReadMessage(); err != nil {
				return
			}
		}
	}()

	return &Session{Conn: client}
}

func verdictFrame(kind string) AuthPacket {
	return AuthPacket{Type: kind, Username: "Alice#abcd", Role: "member", AuthType: "passkey"}
}

func ticketFrame(ticket, url string) AuthPacket {
	return AuthPacket{Type: wire.AuthPair, PairTicket: ticket, PairURL: url, PairTTLSec: 300}
}

// TestReadAuthResultSkipsThePairTicket is the whole point of the loop: the
// ticket frame is consumed and the verdict behind it is what comes back.
func TestReadAuthResultSkipsThePairTicket(t *testing.T) {
	sess := scriptedSocket(t,
		ticketFrame("aa", "https://chat.example.com/web/#pair=aa"),
		verdictFrame(wire.AuthSuccess),
	)
	got, err := readAuthResult(sess, &AuthPacket{Role: "member"})
	if err != nil {
		t.Fatalf("readAuthResult: %v", err)
	}
	if got.Type != wire.AuthSuccess || got.AuthType != "passkey" {
		t.Fatalf("verdict = %+v, want the auth_success behind the ticket", got)
	}
}

// TestReadAuthResultShowsTheLinkToTheUser: the server sends a ticket, and
// the person at the terminal has to end up holding the link. This is the one
// output the whole flow depends on — the browser may be a phone, so nothing
// else reaches them.
func TestReadAuthResultShowsTheLinkToTheUser(t *testing.T) {
	const link = "https://chat.example.com/web/#pair=abcdef"
	sess := scriptedSocket(t,
		ticketFrame("abcdef", link),
		verdictFrame(wire.AuthSuccess),
	)

	out := captureStdout(t, func() {
		if _, err := readAuthResult(sess, &AuthPacket{Role: "member"}); err != nil {
			t.Errorf("readAuthResult: %v", err)
		}
	})

	if !strings.Contains(out, link) {
		t.Errorf("the link never reached the terminal:\n%s", out)
	}
	// The quoted wait comes from the server, and the role is named so a
	// person signing the wrong passkey notices.
	if !strings.Contains(out, "5 phút") {
		t.Errorf("the server's wait budget was not shown:\n%s", out)
	}
	if !strings.Contains(out, "member") {
		t.Errorf("the role was not shown:\n%s", out)
	}
}

// TestReadAuthResultPassesThroughAFailure: a refused handshake has no middle
// step, and its reason must survive to the caller.
func TestReadAuthResultPassesThroughAFailure(t *testing.T) {
	sess := scriptedSocket(t, AuthPacket{Type: wire.AuthFailed, Error: "auth_error: pair_expired"})
	got, err := readAuthResult(sess, &AuthPacket{Role: "member"})
	if err != nil {
		t.Fatalf("readAuthResult: %v", err)
	}
	if got.Type != wire.AuthFailed || !strings.Contains(got.Error, "pair_expired") {
		t.Fatalf("failure = %+v, want the server's reason", got)
	}
}

// TestReadAuthResultRefusesATicketWithoutURL: printing an empty link would
// leave the user with nothing to open and no error to read.
func TestReadAuthResultRefusesATicketWithoutURL(t *testing.T) {
	sess := scriptedSocket(t, AuthPacket{Type: wire.AuthPair, PairTicket: "aa"})
	if _, err := readAuthResult(sess, &AuthPacket{Role: "member"}); err == nil {
		t.Fatal("a pairing ticket with no URL was accepted")
	}
}

// TestReadAuthResultRefusesRepeatedTickets: the server issues at most one
// ticket per handshake. A second one means the connection is not talking to
// this server, and looping on it would wait forever.
func TestReadAuthResultRefusesRepeatedTickets(t *testing.T) {
	sess := scriptedSocket(t,
		ticketFrame("aa", "https://x.example/web/#pair=aa"),
		ticketFrame("bb", "https://x.example/web/#pair=bb"),
		verdictFrame(wire.AuthSuccess),
	)
	if _, err := readAuthResult(sess, &AuthPacket{Role: "member"}); err == nil {
		t.Fatal("a second pairing ticket was accepted")
	}
}

// TestReadAuthResultStopsOnAnUnknownFrame: anything that is neither a ticket
// nor a verdict is refused, so a notice or a stray proxy frame cannot open
// a session that does not exist.
func TestReadAuthResultStopsOnAnUnknownFrame(t *testing.T) {
	sess := scriptedSocket(t, AuthPacket{Type: "notice", Username: "System"})
	got, err := readAuthResult(sess, &AuthPacket{Role: "member"})
	if err != nil {
		t.Fatalf("readAuthResult: %v", err)
	}
	if got.Type == wire.AuthSuccess {
		t.Fatal("an unknown frame was treated as a session")
	}
}

// TestReadAuthResultReportsAClosedSocket: a server that hangs up mid-handshake
// must surface as an error rather than a zero packet that reads like a
// refusal.
func TestReadAuthResultReportsAClosedSocket(t *testing.T) {
	up := websocket.Upgrader{}
	ready := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ready <- conn
		conn.Close()
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	<-ready

	if _, err := readAuthResult(&Session{Conn: client}, &AuthPacket{Role: "member"}); err == nil {
		t.Fatal("a closed socket reported success")
	}
}

// TestValidateAuthFlagsRefusesTwoCredentials: -P owns the pre-auth exchange
// and -K owns the packet body; naming both leaves the role ambiguous.
func TestValidateAuthFlagsRefusesTwoCredentials(t *testing.T) {
	savedPasskey, savedKey := CLI.Passkey, CLI.KeyFile
	t.Cleanup(func() { CLI.Passkey, CLI.KeyFile = savedPasskey, savedKey })

	CLI.Passkey, CLI.KeyFile = "member", "/nonexistent/key.json"
	if err := validateAuthFlags(); err == nil {
		t.Fatal("passkey and key-file together were accepted")
	}

	CLI.KeyFile = ""
	if err := validateAuthFlags(); err != nil {
		t.Fatalf("passkey alone refused: %v", err)
	}

	CLI.Passkey, CLI.KeyFile = "", "/nonexistent/key.json"
	if err := validateAuthFlags(); err != nil {
		t.Fatalf("key-file alone refused: %v", err)
	}
}

// TestPairRequestCarriesNoKeyMaterial: the pairing packet asks for a ceremony
// and nothing else, so there is no assertion material for a confused server
// to mistake for a login attempt.
func TestPairRequestCarriesNoKeyMaterial(t *testing.T) {
	raw, err := json.Marshal(AuthPacket{Type: wire.AuthPairRequest, Role: "member", Username: "A"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"passkey_id", "passkey_sig", "passkey_auth_data", "signature", "hmac"} {
		if _, ok := got[k]; ok {
			t.Errorf("a pairing request carried %q: %s", k, raw)
		}
	}
}
