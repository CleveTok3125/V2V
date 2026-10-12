package clientcore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/pow"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// recordingGate collects the events a round emits, so a test can assert what
// the caller was told as well as what the round returned.
type recordingGate struct{ events []GateEvent }

func (r *recordingGate) OnGate(e GateEvent) { r.events = append(r.events, e) }

func (r *recordingGate) kinds() []GateEventKind {
	out := make([]GateEventKind, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Kind)
	}
	return out
}

func (r *recordingGate) saw(kind GateEventKind) bool {
	for _, e := range r.events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// tinyGateSolve is a real solve at a preset small enough for a test.
func tinyGateSolve(p pow.Preset, salt string, maxCostMs int64) (uint64, error) {
	return pow.Solve(p, salt, make(chan struct{}))
}

// gateTestServer answers the join gate: a challenge that is already open, then
// a submit that verifies the nonce before handing out a lease.
func gateTestServer(t *testing.T, submitStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Step  string `json:"step"`
			Nonce uint64 `json:"nonce"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode %q: %v", r.URL.Path, err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Step {
		case "status":
			_, _ = w.Write([]byte(`{"required":true}`))
		case "challenge":
			_, _ = w.Write([]byte(`{"challenge_id":"c1","tier":1,` +
				`"pow":{"t":1,"m":8192,"p":1,"difficulty":8},` +
				`"salt":"gate-salt","earliest":0,"expires":4102444800,"ticket":"tkt"}`))
		case "submit":
			if submitStatus != http.StatusOK {
				w.WriteHeader(submitStatus)
				return
			}
			preset := pow.Preset{Time: 1, Memory: 8192, Threads: 1, Difficulty: 8}
			if !pow.Verify(preset, "gate-salt", req.Nonce) {
				t.Errorf("submitted nonce %d does not verify", req.Nonce)
			}
			_, _ = w.Write([]byte(`{"pass":"lease-1"}`))
		}
	}))
}

func testGateConfig(out GateOutput) GateConfig {
	return GateConfig{
		Transport: func() (http.RoundTripper, error) { return nil, nil },
		MaxTier:   3,
		MaxCostMs: 60_000,
		Solve:     tinyGateSolve,
		Output:    out,
	}
}

func TestRunGateFlowReturnsTheLease(t *testing.T) {
	srv := gateTestServer(t, http.StatusOK)
	defer srv.Close()
	out := &recordingGate{}
	pass, ok := RunGateFlow(srv.URL, testGateConfig(out))
	if !ok || pass != "lease-1" {
		t.Fatalf("RunGateFlow = %q, %v; want lease-1, true", pass, ok)
	}
	if !out.saw(GateSolving) {
		t.Fatalf("the round must report that it is solving, got %v", out.kinds())
	}
}

func TestGateRequiredNowReadsTheFlag(t *testing.T) {
	srv := gateTestServer(t, http.StatusOK)
	defer srv.Close()
	if !GateRequiredNow(srv.URL, testGateConfig(nil)) {
		t.Fatal("the server said the gate is required")
	}
}

func TestRunGateFlowReportsRefusal(t *testing.T) {
	srv := gateTestServer(t, http.StatusForbidden)
	defer srv.Close()
	out := &recordingGate{}
	if _, ok := RunGateFlow(srv.URL, testGateConfig(out)); ok {
		t.Fatal("a refused submit must not yield a lease")
	}
	if !out.saw(GateRefused) {
		t.Fatalf("the caller must hear about the refusal, got %v", out.kinds())
	}
}

// A tier above the local cap is declined before any work happens, which is the
// point of the cap: a hostile server must not buy a memory-hard solve.
func TestRunGateFlowRefusesAboveTheCapBeforeSolving(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"challenge_id":"c1","tier":9,` +
			`"pow":{"t":1,"m":8192,"p":1,"difficulty":8},` +
			`"salt":"s","earliest":0,"expires":4102444800,"ticket":"tkt"}`))
	}))
	defer srv.Close()
	cfg := testGateConfig(nil)
	cfg.Solve = func(pow.Preset, string, int64) (uint64, error) {
		t.Fatal("an over-cap tier must be refused before solving")
		return 0, nil
	}
	out := &recordingGate{}
	cfg.Output = out
	if _, ok := RunGateFlow(srv.URL, cfg); ok {
		t.Fatal("an over-cap tier must not yield a lease")
	}
	if !out.saw(GateTierRefused) {
		t.Fatalf("the caller must hear about the cap, got %v", out.kinds())
	}
}

// A missing solver is a caller bug, not a server one, and it must be reported
// rather than panicking.
func TestRunGateFlowWithoutASolverReportsFailure(t *testing.T) {
	srv := gateTestServer(t, http.StatusOK)
	defer srv.Close()
	cfg := testGateConfig(nil)
	cfg.Solve = nil
	out := &recordingGate{}
	cfg.Output = out
	if _, ok := RunGateFlow(srv.URL, cfg); ok {
		t.Fatal("a round without a solver cannot succeed")
	}
	if !out.saw(GateSolveFailed) {
		t.Fatalf("the caller must hear that no solver ran, got %v", out.kinds())
	}
}

// A missing transport must fail the round instead of dialling directly.
func TestRunGateFlowWithoutATransportFails(t *testing.T) {
	out := &recordingGate{}
	cfg := testGateConfig(out)
	cfg.Transport = nil
	if _, ok := RunGateFlow("http://127.0.0.1:1", cfg); ok {
		t.Fatal("a round without a transport cannot succeed")
	}
	if !out.saw(GateChallengeFailed) {
		t.Fatalf("the caller must hear about the failed challenge, got %v", out.kinds())
	}
}

func signTestOffer(t *testing.T, o wire.PowOffer) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	po := pow.Offer{
		Tier: o.Tier,
		Preset: pow.Preset{
			Time: o.T, Memory: o.M, Threads: o.P, Difficulty: o.Difficulty,
		},
		Salt: o.Salt, Expires: o.Expires, ChallengeID: o.ChallengeID,
	}
	sig := hex.EncodeToString(pow.SignOffer(priv, po))
	return sig, hex.EncodeToString(pub)
}

func testPowOffer() wire.PowOffer {
	return wire.PowOffer{
		Type: "pow_offer", Tier: 1,
		T: 1, M: 8192, P: 1, Difficulty: 8,
		Salt: "s", Expires: time.Now().Add(time.Minute).Unix(), ChallengeID: "c1",
	}
}

func TestVerifyPowOfferPinned(t *testing.T) {
	o := testPowOffer()
	sig, pub := signTestOffer(t, o)
	o.OfferSig, o.ServerPub = sig, pub
	preset, err := VerifyPowOffer(o, pub, false)
	if err != nil {
		t.Fatal(err)
	}
	if preset.Time != 1 || preset.Memory != 8192 || preset.Difficulty != 8 {
		t.Fatalf("preset mismatch: %+v", preset)
	}
}

func TestVerifyPowOfferTamper(t *testing.T) {
	o := testPowOffer()
	sig, pub := signTestOffer(t, o)
	o.OfferSig, o.ServerPub = sig, pub
	o.Difficulty = 4
	if _, err := VerifyPowOffer(o, pub, false); err == nil {
		t.Fatal("tampered difficulty must fail with pin")
	}
}

func TestVerifyPowOfferTOFU(t *testing.T) {
	o := testPowOffer()
	if _, err := VerifyPowOffer(o, "", false); err != nil {
		t.Fatalf("no pin must proceed TOFU: %v", err)
	}
}

func TestVerifyPowOfferWasmClamp(t *testing.T) {
	o := testPowOffer()
	o.P = 4
	sig, pub := signTestOffer(t, o)
	o.OfferSig, o.ServerPub = sig, pub
	preset, err := VerifyPowOffer(o, pub, true)
	if err != nil {
		t.Fatal(err)
	}
	if preset.Threads != 1 {
		t.Fatalf("wasm must clamp P=1, got %+v", preset)
	}
}

func TestDecideOfferTier(t *testing.T) {
	if DecideOffer(4, 3) {
		t.Fatal("tier above max must decline")
	}
	if !DecideOffer(2, 3) {
		t.Fatal("tier within max must accept")
	}
	if !DecideOffer(3, 3) {
		t.Fatal("tier equal to max must accept")
	}
}

func TestGatePassURL(t *testing.T) {
	for in, want := range map[string]string{
		"ws://h:1/ws":   "http://h:1",
		"wss://h:1/ws":  "https://h:1",
		"wss://h/x/ws":  "https://h/x",
		"ws://h":        "http://h",
		"http://h:1/ws": "http://h:1",
	} {
		if got := GateHTTPBase(in); got != want {
			t.Fatalf("GateHTTPBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseGateChallenge(t *testing.T) {
	body := []byte(`{"challenge_id":"c","tier":1,"pow":{"t":1,"m":8192,"p":1,"difficulty":16},"salt":"s","earliest":1700000000,"expires":1700000600,"ticket":"tkt","offer_sig":"sig","server_pub":"pub"}`)
	c, err := ParseGateChallenge(body)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "c" || c.Tier != 1 || c.Preset.Difficulty != 16 || c.Ticket != "tkt" {
		t.Fatalf("parse mismatch: %+v", c)
	}
	if _, err := ParseGateChallenge([]byte(`{"tier":`)); err == nil {
		t.Fatal("malformed JSON must fail")
	}
}
