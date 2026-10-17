package clientcore

// Join-gate client: fetch a challenge, verify the server's signature against
// a pinned identity, clamp the preset, solve under a wall-clock budget and
// submit. The caller supplies the round tripper, the platform solve path and
// a sink for progress, because the library owns neither the runtime nor the
// wording.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/lib/pow"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// GateChallenge is one /api/join-gate challenge bundle.
type GateChallenge struct {
	ID        string
	Tier      int
	Preset    pow.Preset
	Salt      string
	Earliest  time.Time
	Expires   time.Time
	Ticket    string
	OfferSig  string
	ServerPub string
}

// ParseGateChallenge decodes a challenge bundle. A missing id, salt or ticket
// means the body is not a challenge at all.
func ParseGateChallenge(body []byte) (*GateChallenge, error) {
	var raw struct {
		ChallengeID string `json:"challenge_id"`
		Tier        int    `json:"tier"`
		Pow         struct {
			T          int  `json:"t"`
			M          int  `json:"m"`
			P          int  `json:"p"`
			Difficulty uint `json:"difficulty"`
		} `json:"pow"`
		Salt      string `json:"salt"`
		Earliest  int64  `json:"earliest"`
		Expires   int64  `json:"expires"`
		Ticket    string `json:"ticket"`
		OfferSig  string `json:"offer_sig"`
		ServerPub string `json:"server_pub"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw.ChallengeID == "" || raw.Salt == "" || raw.Ticket == "" {
		return nil, errors.New("incomplete gate challenge")
	}
	return &GateChallenge{
		ID:        raw.ChallengeID,
		Tier:      raw.Tier,
		Preset:    pow.Preset{Time: raw.Pow.T, Memory: raw.Pow.M, Threads: raw.Pow.P, Difficulty: raw.Pow.Difficulty},
		Salt:      raw.Salt,
		Earliest:  time.Unix(raw.Earliest, 0),
		Expires:   time.Unix(raw.Expires, 0),
		Ticket:    raw.Ticket,
		OfferSig:  raw.OfferSig,
		ServerPub: raw.ServerPub,
	}, nil
}

// PlatformName reports the runtime label the gate advertises, which the server
// uses to pick a preset the runtime can actually solve.
func PlatformName(wasm bool) string {
	if wasm {
		return "web"
	}
	return "native"
}

// VerifyPowOffer checks the server signature against the pinned identity,
// then clamps the preset to the platform.
//
// Empty pin means trust-on-first-use. Tampering with tier or difficulty
// breaks the signature.
func VerifyPowOffer(o wire.PowOffer, pin string, wasm bool) (pow.Preset, error) {
	preset := pow.Preset{Time: o.T, Memory: o.M, Threads: o.P, Difficulty: o.Difficulty}
	if pin != "" {
		pub, err := hex.DecodeString(pin)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return pow.Preset{}, errors.New("bad server pin")
		}
		sig, err := hex.DecodeString(o.OfferSig)
		if err != nil {
			return pow.Preset{}, errors.New("bad offer signature")
		}
		po := pow.Offer{
			Tier: o.Tier, Preset: preset,
			Salt: o.Salt, Expires: o.Expires, ChallengeID: o.ChallengeID,
		}
		if !pow.VerifyOffer(ed25519.PublicKey(pub), po, sig) {
			return pow.Preset{}, errors.New("offer signature mismatch")
		}
	}
	clamped, _ := pow.Clamp(preset, wasm)
	return clamped, nil
}

// DecideOffer accepts tiers within the local cap.
func DecideOffer(tier, maxTier int) bool { return tier <= maxTier }

// GateHTTPBase strips the WS path and scheme down to the HTTP origin, so the
// gate endpoint stays on the same server the client dials.
func GateHTTPBase(wsURL string) string {
	base := wsURL
	if i := strings.Index(base, "://"); i >= 0 {
		scheme, rest := base[:i], base[i+3:]
		httpScheme := "http"
		if scheme == "wss" || scheme == "https" {
			httpScheme = "https"
		}
		base = httpScheme + "://" + rest
	}
	base = strings.TrimRight(base, "/")
	if i := strings.Index(base, "/"); i >= 0 {
		// Keep host only when the path is exactly the WS endpoint;
		// otherwise preserve sub-path deployments.
		if strings.HasSuffix(base, "/ws") {
			return strings.TrimSuffix(base, "/ws")
		}
		return base
	}
	return base
}

// GateEventKind labels one step of a gate round.
type GateEventKind int

const (
	GateChallengeFailed GateEventKind = iota
	GateChallengeMalformed
	GateOfferInvalid
	GateTierRefused
	GateSolving
	GateSolveFailed
	GateWaiting
	GateSubmitFailed
	GatePassMalformed
	GateRefused
)

// GateEvent reports one gate step to the caller, which owns the wording.
type GateEvent struct {
	Kind   GateEventKind
	Status int
	Err    error
	Tier   int
	Cap    int
	Wait   time.Duration
}

// GateOutput receives gate progress and failures.
type GateOutput interface{ OnGate(GateEvent) }

// GateConfig carries what the gate needs from its caller.
type GateConfig struct {
	// Transport builds the round tripper gate requests ride. The join gate
	// is reached before the session dial, so these requests have to ride the
	// session proxy too, or a proxied client leaks its real address on
	// exactly the requests that carry its proof of work.
	Transport func() (http.RoundTripper, error)
	// Pin is the pinned server pubkey; empty means trust-on-first-use.
	Pin string
	// WASM selects the runtime the preset is clamped for.
	WASM bool
	// MaxTier and MaxCostMs are the local budget.
	MaxTier   int
	MaxCostMs int64
	// Solve is the platform solve path, run under the wall-clock budget so a
	// malicious offer cannot burn the machine.
	Solve  func(pow.Preset, string, int64) (uint64, error)
	Output GateOutput
}

// post sends a bounded JSON body and returns the bounded response body with
// its status. Both directions are capped because both are server-supplied.
func (cfg GateConfig) post(url string, body any, timeout time.Duration) ([]byte, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	if cfg.Transport == nil {
		return nil, 0, errors.New("no gate transport configured")
	}
	tr, err := cfg.Transport()
	if err != nil {
		return nil, 0, err
	}
	client := &http.Client{Transport: tr, Timeout: timeout}
	resp, err := client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, 0, err
	}
	return out, resp.StatusCode, nil
}

func (cfg GateConfig) emit(e GateEvent) {
	if cfg.Output != nil {
		cfg.Output.OnGate(e)
	}
}

// gateStatus is the join-gate status body. Two callers read it: the dial, on
// the 429 that refuses a handshake, and GateRequiredNow, on a plain 200. It is
// one type because it is one contract on one endpoint.
type gateStatus struct {
	Required bool `json:"required"`
}

// GateRequiredNow asks the server whether the join gate is active.
func GateRequiredNow(httpBase string, cfg GateConfig) bool {
	body, status, err := cfg.post(httpBase+"/api/join-gate",
		map[string]any{"step": "status", "ui": PlatformName(cfg.WASM)}, 15*time.Second)
	if err != nil || status != http.StatusOK {
		return false
	}
	var out gateStatus
	if json.Unmarshal(body, &out) != nil {
		return false
	}
	return out.Required
}

// RunGateFlow performs one join-gate round: challenge, verify, solve, wait for
// earliest, submit, with one early retry. It returns the lease pass on success.
func RunGateFlow(httpBase string, cfg GateConfig) (string, bool) {
	body, status, err := cfg.post(httpBase+"/api/join-gate",
		map[string]any{"step": "challenge", "ui": PlatformName(cfg.WASM)}, 30*time.Second)
	if err != nil || status != http.StatusOK {
		cfg.emit(GateEvent{Kind: GateChallengeFailed, Status: status, Err: err})
		return "", false
	}
	chal, err := ParseGateChallenge(body)
	if err != nil {
		cfg.emit(GateEvent{Kind: GateChallengeMalformed, Err: err})
		return "", false
	}
	preset, err := VerifyPowOffer(wire.PowOffer{
		Tier: chal.Tier, T: chal.Preset.Time, M: chal.Preset.Memory,
		P: chal.Preset.Threads, Difficulty: chal.Preset.Difficulty,
		Salt: chal.Salt, Expires: chal.Expires.Unix(), ChallengeID: chal.ID,
		OfferSig: chal.OfferSig, ServerPub: chal.ServerPub,
	}, cfg.Pin, cfg.WASM)
	if err != nil {
		cfg.emit(GateEvent{Kind: GateOfferInvalid, Err: err})
		return "", false
	}
	if !DecideOffer(chal.Tier, cfg.MaxTier) {
		cfg.emit(GateEvent{Kind: GateTierRefused, Tier: chal.Tier, Cap: cfg.MaxTier})
		return "", false
	}
	cfg.emit(GateEvent{Kind: GateSolving, Tier: chal.Tier})
	if cfg.Solve == nil {
		cfg.emit(GateEvent{Kind: GateSolveFailed, Err: errors.New("no gate solver configured")})
		return "", false
	}
	nonce, err := cfg.Solve(preset, chal.Salt, cfg.MaxCostMs)
	if err != nil {
		cfg.emit(GateEvent{Kind: GateSolveFailed, Err: err})
		return "", false
	}
	for attempt := 0; attempt < 2; attempt++ {
		if wait := time.Until(chal.Earliest); wait > 0 {
			cfg.emit(GateEvent{Kind: GateWaiting, Wait: wait})
			time.Sleep(wait)
		}
		sub, _ := json.Marshal(map[string]any{
			"step": "submit", "challenge_id": chal.ID,
			"nonce": nonce, "ticket": chal.Ticket,
		})
		respBody, status, err := cfg.post(httpBase+"/api/join-gate", json.RawMessage(sub), 30*time.Second)
		if err != nil {
			cfg.emit(GateEvent{Kind: GateSubmitFailed, Err: err})
			return "", false
		}
		if status == http.StatusOK {
			var out struct {
				Pass string `json:"pass"`
			}
			if err := json.Unmarshal(respBody, &out); err != nil || out.Pass == "" {
				cfg.emit(GateEvent{Kind: GatePassMalformed})
				return "", false
			}
			return out.Pass, true
		}
		var early struct {
			RetryAfter int `json:"retry_after"`
		}
		if status == http.StatusTooManyRequests && json.Unmarshal(respBody, &early) == nil && early.RetryAfter > 0 && attempt == 0 {
			time.Sleep(time.Duration(early.RetryAfter) * time.Second)
			continue
		}
		cfg.emit(GateEvent{Kind: GateRefused, Status: status})
		return "", false
	}
	return "", false
}
