// Package chain implements the global message log as a hash chain
// ("blockchain-lite"): every chat message links to the previous one, so
// altering any byte of any message (content, author, timestamp or ID)
// breaks the link and every link after it. No per-user signing is needed
// for tamper evidence; trip signatures keep their authorship role on top.
//
// Wire encoding of the link lives in WireMessage (chain_prev, chain_hash,
// chain_height); this package only hashes and verifies.
package chain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// Domain separators keep each use in its own hash domain.
const (
	domainLink    = "V2V-chain-v1"
	domainGenesis = "V2V-chain-genesis"
	domainLegacy  = "V2V-chain-legacy-anchor"
)

// Hash links one message into the log. Fields are length-prefixed so
// concatenation is unambiguous regardless of embedded separators. tmpID
// is covered too, so renumbering breaks the link for every observer.
func Hash(prev [32]byte, height uint64, tmpID uint64, typ, tim, display, text, tripSig string) [32]byte {
	h := sha256.New()
	h.Write([]byte(domainLink))
	h.Write([]byte{0})
	var buf [binary.MaxVarintLen64]byte
	put := func(b []byte) {
		n := binary.PutUvarint(buf[:], uint64(len(b)))
		h.Write(buf[:n])
		h.Write(b)
	}
	put(prev[:])
	n := binary.PutUvarint(buf[:], height)
	h.Write(buf[:n])
	n = binary.PutUvarint(buf[:], tmpID)
	h.Write(buf[:n])
	put([]byte(typ))
	put([]byte(tim))
	put([]byte(display))
	put([]byte(text))
	put([]byte(tripSig))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Genesis derives the chain seed from the server identity. It is
// deterministic so a server restart resumes the same chain without
// persisting extra state (server_identity.json already persists).
func Genesis(serverPubHex string) [32]byte {
	return sha256.Sum256([]byte(domainGenesis + "\x00" + strings.ToLower(strings.TrimSpace(serverPubHex))))
}

// LegacyAnchor anchors pre-chain history without rewriting it: the first
// chained message after an upgrade links to the anchor instead of faking
// links for legacy records.
func LegacyAnchor(lastLegacyMsg string) [32]byte {
	return sha256.Sum256([]byte(domainLegacy + "\x00" + lastLegacyMsg))
}

// VerifyLink recomputes the link and compares it with want.
func VerifyLink(prev [32]byte, height uint64, tmpID uint64, typ, tim, display, text, tripSig string, want [32]byte) bool {
	return Hash(prev, height, tmpID, typ, tim, display, text, tripSig) == want
}

// ParseHex64 decodes a 64-char lowercase hex hash; it reports false for
// any malformed input instead of failing open.
func ParseHex64(s string) ([32]byte, bool) {
	var out [32]byte
	if len(s) != 64 {
		return out, false
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return out, false
	}
	copy(out[:], b)
	return out, true
}

// Short renders 8 hex chars for logs; Short4 renders 4 for the
// "#height:hash" chat meta line.
func Short(h [32]byte) string {
	return hex.EncodeToString(h[:])[:8]
}

// Short4 renders 4 hex chars for the "#height:hash" chat meta line.
func Short4(h [32]byte) string {
	return hex.EncodeToString(h[:])[:4]
}
