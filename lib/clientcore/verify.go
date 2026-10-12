package clientcore

// Chain and trip verdicts over a wire, with no tip context and no view: what
// a caller needs to decide whether a message is intact, before it decides how
// to show it.

import (
	"encoding/hex"
	"strings"

	"github.com/CleveTok3125/V2V/lib/chain"
	"github.com/CleveTok3125/V2V/lib/tripcolor"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// ChainLinkError reports a link that does not hold. ErrPrevMismatch marks the
// running-tip break specifically: a forward height jump with that error is a
// transit gap (dropped frames), while any other link error is a tamper signal.
type ChainLinkError struct{ Msg string }

func (e *ChainLinkError) Error() string { return "chain link broken: " + e.Msg }

// ErrPrevMismatch is the running-tip break: the wire does not continue the
// local tip.
var ErrPrevMismatch = &ChainLinkError{Msg: "prev does not continue the local tip"}

// VerifyWireContent recomputes a wire link from its own fields, without any
// tip context: it proves the content is intact but says nothing about the
// wire's position in the log, which needs the running tip.
func VerifyWireContent(msg wire.WireMessage) error {
	want, ok := chain.ParseHex64(msg.ChainHash)
	if !ok {
		return &ChainLinkError{Msg: "malformed chain_hash"}
	}
	prev, ok := chain.ParseHex64(msg.ChainPrev)
	if !ok {
		return &ChainLinkError{Msg: "malformed chain_prev"}
	}
	if !chain.VerifyWire(prev, msg, want) {
		return &ChainLinkError{Msg: "hash does not match content"}
	}
	return nil
}

// TripPayloadBytes rebuilds the exact bytes a trip signature covers, so a
// caller can check the signature itself. It returns nil when a hex input is
// malformed, which a caller reads as "cannot verify".
func TripPayloadBytes(msg wire.WireMessage, tm *wire.TripMeta) []byte {
	lower := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	pub, err1 := hex.DecodeString(lower(tm.Pub))
	prev, err2 := hex.DecodeString(lower(tm.Prev))
	msgHash, err3 := hex.DecodeString(lower(tm.MsgHash))
	if err1 != nil || err2 != nil || err3 != nil {
		return nil
	}
	return tripcolor.CanonicalPayload(lower(tm.ServerPub), tm.Seq, prev, msgHash, pub, msg.DisplayName, tm.TmpID, tm.ReplyTo)
}
