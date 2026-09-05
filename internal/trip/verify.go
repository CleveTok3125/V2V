package trip

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/CleveTok3125/V2V/internal/tripcolor"
)

// VerifyParams collects all fields needed to verify a trip signature.
// TmpID binds the sender's per-session message counter into the
// signature, so altering it breaks verification.
type VerifyParams struct {
	Text        string
	DisplayName string
	ServerPub   string
	PubHex      string
	Seq         uint32
	PrevHex     string
	SigHex      string
	MsgHashHex  string
	TmpID       uint64
}

// VerifyResult holds successful verification data.
type VerifyResult struct {
	PubHex    string
	Seq       uint32
	PrevHex   string
	SigHex    string
	MsgHash   string
	ServerPub string
	Badge     string
	NewPrev   []byte
}

func Verify(p VerifyParams) (*VerifyResult, error) {
	pubHex := strings.ToLower(strings.TrimSpace(p.PubHex))
	sigHex := strings.ToLower(strings.TrimSpace(p.SigHex))
	prevHex := strings.ToLower(strings.TrimSpace(p.PrevHex))
	msgHashHex := strings.ToLower(strings.TrimSpace(p.MsgHashHex))
	serverPub := strings.ToLower(strings.TrimSpace(p.ServerPub))

	if len(pubHex) != 64 {
		return nil, fmt.Errorf("invalid pub length")
	}
	if len(sigHex) != 128 {
		return nil, fmt.Errorf("invalid sig length")
	}
	if len(prevHex) != 64 {
		return nil, fmt.Errorf("invalid prev length")
	}
	if len(msgHashHex) != 64 {
		return nil, fmt.Errorf("invalid msg_hash length")
	}
	if len(serverPub) != 0 && len(serverPub) != 64 {
		return nil, fmt.Errorf("invalid server_pub length")
	}

	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("bad pub hex")
	}
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return nil, fmt.Errorf("bad sig hex")
	}
	prevBytes, err := hex.DecodeString(prevHex)
	if err != nil || len(prevBytes) != 32 {
		return nil, fmt.Errorf("bad prev hex")
	}
	msgHashBytes, err := hex.DecodeString(msgHashHex)
	if err != nil || len(msgHashBytes) != 32 {
		return nil, fmt.Errorf("bad msg_hash hex")
	}
	// Recompute msg hash from text and compare
	actual := sha256.Sum256([]byte(p.Text))
	if hex.EncodeToString(actual[:]) != msgHashHex {
		return nil, fmt.Errorf("msg_hash mismatch")
	}
	// TmpID 0 means a pre-chain signature (or a legacy browser link
	// without tmp_id): verify against the legacy payload encoding so old
	// history and old links keep verifying read-only. New signatures
	// always bind a nonzero TmpID and never match the legacy encoding,
	// so the fallback cannot be abused to strip IDs.
	payload := tripcolor.CanonicalPayload(serverPub, p.Seq, prevBytes, msgHashBytes, pubBytes, p.DisplayName, p.TmpID)
	if p.TmpID == 0 {
		payload = tripcolor.CanonicalPayloadLegacy(serverPub, p.Seq, prevBytes, msgHashBytes, pubBytes, p.DisplayName)
	}
	if !ed25519.Verify(pubBytes, payload, sigBytes) {
		return nil, fmt.Errorf("signature mismatch")
	}
	h := sha256.New()
	h.Write(prevBytes)
	h.Write(sigBytes)
	h.Write(msgHashBytes)
	newPrev := h.Sum(nil)
	// badge
	ph := sha256.Sum256(pubBytes)
	badge := "◆ " + hex.EncodeToString(ph[:])[:8]
	return &VerifyResult{
		PubHex:    pubHex,
		Seq:       p.Seq,
		PrevHex:   prevHex,
		SigHex:    sigHex,
		MsgHash:   msgHashHex,
		ServerPub: serverPub,
		Badge:     badge,
		NewPrev:   newPrev,
	}, nil
}
