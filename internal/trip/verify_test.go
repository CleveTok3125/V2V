package trip

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/CleveTok3125/V2V/internal/tripcolor"
)

func signFixture(t *testing.T, tmpID uint64, replyTo uint64, legacy bool) (VerifyParams, []byte) {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 3)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	serverPub := hex.EncodeToString(make([]byte, 32))
	var prev [32]byte
	text := "chain binding probe"
	msgHash := sha256.Sum256([]byte(text))
	var payload []byte
	if legacy {
		payload = tripcolor.CanonicalPayloadLegacy(serverPub, 1, prev[:], msgHash[:], pub, "Bob")
	} else {
		payload = tripcolor.CanonicalPayload(serverPub, 1, prev[:], msgHash[:], pub, "Bob", tmpID, replyTo)
	}
	sig := ed25519.Sign(priv, payload)
	return VerifyParams{
		Text:        text,
		DisplayName: "Bob",
		ServerPub:   serverPub,
		PubHex:      hex.EncodeToString(pub),
		Seq:         1,
		PrevHex:     hex.EncodeToString(prev[:]),
		SigHex:      hex.EncodeToString(sig),
		MsgHashHex:  hex.EncodeToString(msgHash[:]),
		TmpID:       tmpID,
		ReplyTo:     replyTo,
	}, pub
}

func TestVerifyBindsTmpID(t *testing.T) {
	p, _ := signFixture(t, 7, 0, false)
	if _, err := Verify(p); err != nil {
		t.Fatalf("valid bound signature must verify: %v", err)
	}
	// Server renumbering the session counter breaks the signature.
	p.TmpID = 8
	if _, err := Verify(p); err == nil {
		t.Fatal("renumbered tmp_id must not verify")
	}
}

func TestVerifyLegacyFallback(t *testing.T) {
	// Pre-chain signatures (no tmpID bound) keep verifying read-only so
	// old history and legacy browser links do not turn red on upgrade.
	p, _ := signFixture(t, 0, 0, true)
	if _, err := Verify(p); err != nil {
		t.Fatalf("legacy signature must verify: %v", err)
	}
	// A new signature presented without tmp_id must NOT fall back: the
	// payloads differ, so stripping the ID fails instead of verifying.
	p2, _ := signFixture(t, 7, 0, false)
	p2.TmpID = 0
	if _, err := Verify(p2); err == nil {
		t.Fatal("stripped tmp_id must not verify a new signature")
	}
}

func TestVerifyBindsReplyTo(t *testing.T) {
	p, _ := signFixture(t, 7, 42, false)
	if _, err := Verify(p); err != nil {
		t.Fatalf("valid reply-bound signature must verify: %v", err)
	}
	p.ReplyTo = 43
	if _, err := Verify(p); err == nil {
		t.Fatal("swapped reply_to must not verify")
	}
}
