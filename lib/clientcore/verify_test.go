package clientcore

import (
	"encoding/hex"
	"testing"

	"github.com/CleveTok3125/V2V/lib/chain"
	"github.com/CleveTok3125/V2V/lib/wire"
)

func chainedWire(prev [32]byte, height, tmpID uint64) wire.WireMessage {
	h := chain.Hash(prev, height, tmpID, 0, "chat", "15:04", "Alice", "hello", "")
	return wire.WireMessage{
		Type: "chat", Time: "15:04", DisplayName: "Alice", Text: "hello",
		TmpID:       tmpID,
		ChainPrev:   hex.EncodeToString(prev[:]),
		ChainHash:   hex.EncodeToString(h[:]),
		ChainHeight: height,
		ChainVer:    2,
	}
}

func TestVerifyWireContentAcceptsAnIntactWire(t *testing.T) {
	if err := VerifyWireContent(chainedWire(chain.Genesis("srv"), 1, 5)); err != nil {
		t.Fatalf("intact wire must verify: %v", err)
	}
}

// A display-only stamp is not part of the link, so adding one must not break
// already-stored history.
func TestVerifyWireContentIgnoresSentAt(t *testing.T) {
	msg := chainedWire(chain.Genesis("srv"), 1, 5)
	msg.SentAt = "2026-09-27T03:27:45+07:00"
	if err := VerifyWireContent(msg); err != nil {
		t.Fatalf("sent_at must not break verification: %v", err)
	}
}

func TestVerifyWireContentRejectsTampering(t *testing.T) {
	msg := chainedWire(chain.Genesis("srv"), 1, 5)
	msg.Text = "tampered"
	err := VerifyWireContent(msg)
	if err == nil {
		t.Fatal("tampered text must fail verification")
	}
	var linkErr *ChainLinkError
	if !asChainLink(err, &linkErr) || linkErr.Msg != "hash does not match content" {
		t.Fatalf("err = %v, want a hash mismatch", err)
	}
}

func TestVerifyWireContentRejectsMalformedHex(t *testing.T) {
	msg := chainedWire(chain.Genesis("srv"), 1, 5)
	msg.ChainHash = "zz"
	if err := VerifyWireContent(msg); err == nil {
		t.Fatal("malformed hex must fail verification")
	}
}

func asChainLink(err error, target **ChainLinkError) bool {
	e, ok := err.(*ChainLinkError)
	if ok {
		*target = e
	}
	return ok
}
