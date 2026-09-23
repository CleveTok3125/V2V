package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/serverconfig"
	"github.com/CleveTok3125/V2V/internal/trustedproxy"
)

// withOnion swaps the static onion config for the duration of a test.
func withOnion(t *testing.T, hosts []string, allowWeb, allowPasskey bool) {
	t.Helper()
	old := Cfg.Static
	Cfg.Static.Onion = OnionConfig{Hosts: hosts, AllowWeb: allowWeb, AllowPasskey: allowPasskey}
	t.Cleanup(func() { Cfg.Static = old })
}

func onionNets(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func TestParseOnionHosts(t *testing.T) {
	got, err := serverconfig.ParseOnionHosts(" AbCd.Onion , dup.onion, ABCD.onion:8080, , trail.onion.")
	if err != nil {
		t.Fatalf("valid list must parse: %v", err)
	}
	want := []string{"abcd.onion", "dup.onion", "trail.onion"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if out, err := serverconfig.ParseOnionHosts("  "); err != nil || len(out) != 0 {
		t.Fatalf("blank list must be empty: %v %v", out, err)
	}
	if _, err := serverconfig.ParseOnionHosts("example.com"); err == nil {
		t.Fatal("non-.onion entry must be a boot error")
	}
	if _, err := serverconfig.ParseOnionHosts("abcd.onion,evil.com"); err == nil {
		t.Fatal("mixed list must fail on the non-.onion entry")
	}
}

func TestIsOnionHost(t *testing.T) {
	cfg := &StaticConfig{Onion: OnionConfig{Hosts: []string{"abcd.onion"}}}
	for _, host := range []string{"abcd.onion", "ABCD.ONION", "abcd.onion:80", "abcd.onion."} {
		if !cfg.IsOnionHost(host) {
			t.Fatalf("host %q must match", host)
		}
	}
	for _, host := range []string{"", "other.onion", "abcd.onion.evil.com"} {
		if cfg.IsOnionHost(host) {
			t.Fatalf("host %q must not match", host)
		}
	}
	empty := &StaticConfig{}
	if empty.OnionEnabled() || empty.IsOnionHost("abcd.onion") {
		t.Fatal("no hosts configured means onion disabled")
	}
}

func onionReq(host, remote string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/ws", nil)
	r.Host = host
	r.RemoteAddr = remote
	return r
}

func TestIsSecuredConnectOnion(t *testing.T) {
	testCfg(t)
	oldTLS := Cfg.Static.RequireTLS
	Cfg.Static.RequireTLS = true
	t.Cleanup(func() { Cfg.Static.RequireTLS = oldTLS })
	withOnion(t, []string{"abcd.onion"}, false, false)
	Cfg.Static.Onion.Trust = onionNets(t, "10.0.0.5/32")

	// Onion host from loopback (tor on the host) is secure.
	r := onionReq("abcd.onion", "127.0.0.1:5555")
	rec := httptest.NewRecorder()
	loop := trustedproxy.Outcome{ClientIP: "127.0.0.1", RemoteIP: "127.0.0.1", Provider: "none"}
	if !IsSecuredConnect(rec, r, loop) {
		t.Fatal("onion from loopback must be secure")
	}

	// Onion host from an onion-trusted hop (tor in a container) is secure.
	rd := onionReq("abcd.onion", "10.0.0.5:5555")
	recd := httptest.NewRecorder()
	hop := trustedproxy.Outcome{ClientIP: "10.0.0.5", RemoteIP: "10.0.0.5", Provider: "direct"}
	if !IsSecuredConnect(recd, rd, hop) {
		t.Fatal("onion from a trusted hop must be secure")
	}

	// Onion host from an untrusted public remote must be rejected.
	rp := onionReq("abcd.onion", "198.51.100.9:5555")
	recp := httptest.NewRecorder()
	public := trustedproxy.Outcome{ClientIP: "198.51.100.9", RemoteIP: "198.51.100.9", Provider: "none"}
	if IsSecuredConnect(recp, rp, public) {
		t.Fatal("onion host from public remote must not be secure")
	}
	if recp.Code != http.StatusUpgradeRequired {
		t.Fatalf("code = %d, want 426", recp.Code)
	}

	// A direct-trusted address outside the onion trust list is not a hop.
	ro := onionReq("abcd.onion", "10.0.0.6:5555")
	reco := httptest.NewRecorder()
	otherDirect := trustedproxy.Outcome{ClientIP: "10.0.0.6", RemoteIP: "10.0.0.6", Provider: "direct"}
	if IsSecuredConnect(reco, ro, otherDirect) {
		t.Fatal("direct.txt membership alone must not grant onion without onion.txt")
	}

	// Onion host through a header-trusted proxy must not qualify.
	rx := onionReq("abcd.onion", "198.51.100.9:5555")
	recx := httptest.NewRecorder()
	proxied := trustedproxy.Outcome{ClientIP: "203.0.113.7", RemoteIP: "198.51.100.9", Provider: "cloudflare", Trusted: true}
	if IsSecuredConnect(recx, rx, proxied) {
		t.Fatal("onion via header-trusted proxy must not be secure")
	}

	// Non-onion host without TLS stays rejected.
	rn := onionReq("example.com", "198.51.100.9:5555")
	recn := httptest.NewRecorder()
	if IsSecuredConnect(recn, rn, public) {
		t.Fatal("plain host without TLS must not be secure")
	}
	if recn.Code != http.StatusUpgradeRequired {
		t.Fatalf("code = %d, want 426", recn.Code)
	}
}

func TestOnionFeatureGates(t *testing.T) {
	testCfg(t)
	// The web master switch is on for these cases; the onion layer is
	// what they exercise.
	Cfg.Static.WebEnabled = true
	onionR := onionReq("abcd.onion", "127.0.0.1:5555")
	plainR := onionReq("example.com", "198.51.100.9:5555")

	withOnion(t, []string{"abcd.onion"}, false, false)
	if webAllowed(onionR) || passkeyAllowed(onionR) {
		t.Fatal("onion must deny web and passkey by default")
	}
	if !webAllowed(plainR) || !passkeyAllowed(plainR) {
		t.Fatal("non-onion requests are unaffected")
	}
	if !passkeyDisabledForOnion(true) || passkeyDisabledForOnion(false) {
		t.Fatal("passkeyDisabledForOnion must track the onion restriction")
	}

	withOnion(t, []string{"abcd.onion"}, true, true)
	if !webAllowed(onionR) || !passkeyAllowed(onionR) {
		t.Fatal("explicit opt-in must allow web and passkey")
	}
	if passkeyDisabledForOnion(true) {
		t.Fatal("opt-in must clear the onion passkey restriction")
	}

	withOnion(t, nil, true, true)
	if !webAllowed(onionR) || !passkeyAllowed(onionR) {
		t.Fatal("no onion hosts means no restriction")
	}
}

// TestWebEnabledMasterSwitch pins the two-layer web gate: WEB_ENABLED is
// the master switch for every request, ONION_ALLOW_WEB only adds the
// onion restriction on top. With WEB_ENABLED=false nothing is served,
// even for a non-onion host or an onion opt-in.
func TestWebEnabledMasterSwitch(t *testing.T) {
	testCfg(t)
	onionR := onionReq("abcd.onion", "127.0.0.1:5555")
	plainR := onionReq("example.com", "198.51.100.9:5555")

	withWeb := func(enabled bool) {
		old := Cfg.Static.WebEnabled
		Cfg.Static.WebEnabled = enabled
		t.Cleanup(func() { Cfg.Static.WebEnabled = old })
	}

	withOnion(t, []string{"abcd.onion"}, true, true)

	withWeb(false)
	if webAllowed(plainR) {
		t.Fatal("WEB_ENABLED=false must deny non-onion requests")
	}
	if webAllowed(onionR) {
		t.Fatal("WEB_ENABLED=false must deny onion even with ONION_ALLOW_WEB=true")
	}

	withWeb(true)
	if !webAllowed(plainR) {
		t.Fatal("WEB_ENABLED=true must allow non-onion requests")
	}
	if !webAllowed(onionR) {
		t.Fatal("WEB_ENABLED=true with ONION_ALLOW_WEB=true must allow onion")
	}
}

func TestInitOnionTrust(t *testing.T) {
	dir := t.TempDir()
	if nets, err := initOnionTrust(dir); err != nil || nets != nil {
		t.Fatalf("missing onion.txt must be tolerated (loopback-only): %v %v", nets, err)
	}
	path := filepath.Join(dir, "onion.txt")
	if err := os.WriteFile(path, []byte("10.0.0.5/32\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nets, err := initOnionTrust(dir)
	if err != nil || len(nets) != 1 {
		t.Fatalf("valid onion.txt must load one net: %v %v", nets, err)
	}
	if err := os.WriteFile(path, []byte("not-an-ip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := initOnionTrust(dir); err == nil {
		t.Fatal("malformed onion.txt must fail the boot")
	}
}

func TestHandleAuthPasskeyDisabledOnOnion(t *testing.T) {
	testCfg(t)
	withOnion(t, []string{"abcd.onion"}, false, false)
	oldEnabled := WAConfig.Enabled
	t.Cleanup(func() { WAConfig.Enabled = oldEnabled })

	run := func(onion bool) error {
		s := NewChatServer()
		client, server := dialAuthPair(t, s)
		done := make(chan error, 1)
		go func() {
			_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", onion)
			done <- err
		}()
		ch := readChallenge(t, client)
		if err := client.WriteJSON(AuthPacket{
			Type: "auth", Username: "Alice", Nonce: ch.Nonce,
			PasskeyID: "x", PasskeySig: "y",
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("HandleAuth hung on onion passkey path")
			return nil
		}
	}

	WAConfig.Enabled = true
	if err := run(true); !errors.Is(err, ErrPasskeyDisabled) {
		t.Fatalf("onion passkey with master on must be disabled, got %v", err)
	}
	// Master off keeps it disabled regardless of the onion flag.
	WAConfig.Enabled = false
	if err := run(true); !errors.Is(err, ErrPasskeyDisabled) {
		t.Fatalf("master-off must disable passkey, got %v", err)
	}

	// Opt-in clears the onion gate; the request then fails later for a
	// different reason (no matching role/credential).
	WAConfig.Enabled = true
	withOnion(t, []string{"abcd.onion"}, true, true)
	if err := run(true); errors.Is(err, ErrPasskeyDisabled) {
		t.Fatalf("opt-in must clear the onion passkey gate, got %v", err)
	}
}
