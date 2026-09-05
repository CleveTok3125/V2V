package main

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"time"

	"github.com/CleveTok3125/V2V/internal/chain"
)

// Global message chain ("blockchain-lite"): every broadcast message links
// to the previous one, so altering any byte of any message breaks the link
// and every link after it. No per-user signing is needed for tamper
// evidence; trip signatures keep their authorship role on top.
//
// Ordering rule: the server defines the order at link time. Link, store
// and send happen under BroadcastMu so every client receives messages in
// chain order and prev-continuity checks never false-positive on reorder.

// chainVersion is the link encoding in use. Version 1 (no replyTo
// segment) verifies pre-reply records; new links always carry 2.
const chainVersion = 2

// tripSigOf extracts the trip signature bound into a wire for chaining.
func tripSigOf(wire WireMessage) string {
	if wire.Trip != nil {
		return wire.Trip.Sig
	}
	return ""
}

// linkAndStore chains one wire message, stores it in memory + disk, and
// returns the chained copy for broadcast. Callers must hold BroadcastMu;
// this takes HistoryMu internally (leaf lock, never the reverse order).
func (s *ChatServer) linkAndStore(wire WireMessage) WireMessage {
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()
	if !s.chainReady {
		s.initChainLocked()
	}
	s.chainHeight++
	prev := s.chainTip
	h := chain.Hash(prev, s.chainHeight, wire.TmpID, wire.ReplyTo, wire.Type, wire.Time, wire.DisplayName, wire.Text, tripSigOf(wire))
	wire.ChainPrev = hex.EncodeToString(prev[:])
	wire.ChainHash = hex.EncodeToString(h[:])
	wire.ChainHeight = s.chainHeight
	wire.ChainVer = chainVersion
	s.chainTip = h
	data, _ := json.Marshal(wire)
	s.appendMessageLocked(string(data))
	if s.HistoryStore != nil {
		s.HistoryStore.EnqueueWire(wire, time.Now().In(Cfg.Static.Timezone))
	}
	return wire
}

// initChainLocked resumes the tip from stored history or starts a new
// chain. Genesis derives from the server identity, so restarts resume the
// same chain without extra state. Pre-chain legacy records are anchored,
// never rewritten: the first chained message links to an anchor over the
// last legacy line. Caller must hold HistoryMu.
func (s *ChatServer) initChainLocked() {
	var serverPub string
	if s.ServerID != nil {
		serverPub = s.ServerID.PublicKey
	}
	tip := chain.Genesis(serverPub)
	var height uint64
	var anchor string
	var anchored bool
	broken := false
	for _, msgStr := range s.ChatHistory {
		var wire WireMessage
		if err := json.Unmarshal([]byte(msgStr), &wire); err != nil || wire.ChainHash == "" {
			// Legacy record: remember as anchor candidate, keep scanning.
			anchor = msgStr
			anchored = true
			continue
		}
		prev, ok1 := chain.ParseHex64(wire.ChainPrev)
		want, ok2 := chain.ParseHex64(wire.ChainHash)
		if !ok1 || !ok2 {
			broken = true
			log.Printf("⛔ [CHAIN TAMPER] height %d: malformed link fields", wire.ChainHeight)
			continue
		}
		expectPrev := tip
		if anchored {
			expectPrev = chain.LegacyAnchor(anchor)
			anchored = false
		}
		if prev != expectPrev || wire.ChainHeight != height+1 {
			broken = true
			log.Printf("⛔ [CHAIN TAMPER] height %d: link break, adopting tip anyway (chat stays up; clients holding older tips flag the fork)", wire.ChainHeight)
			tip, height = want, wire.ChainHeight
			continue
		}
		var linked bool
		if wire.ChainVer >= 2 {
			linked = chain.VerifyLink(prev, wire.ChainHeight, wire.TmpID, wire.ReplyTo, wire.Type, wire.Time, wire.DisplayName, wire.Text, tripSigOf(wire), want)
		} else {
			linked = chain.VerifyLinkV1(prev, wire.ChainHeight, wire.TmpID, wire.Type, wire.Time, wire.DisplayName, wire.Text, tripSigOf(wire), want)
		}
		if !linked {
			broken = true
			log.Printf("⛔ [CHAIN TAMPER] height %d: link break, adopting tip anyway (chat stays up; clients holding older tips flag the fork)", wire.ChainHeight)
		}
		tip, height = want, wire.ChainHeight
	}
	if broken {
		log.Printf("⛔ [CHAIN TAMPER] history failed verification on load; tip adopted, online clients detect forks via persisted tips")
	}
	if anchored && height == 0 {
		// Legacy-only log: the next message anchors to the last legacy
		// line instead of genesis.
		tip = chain.LegacyAnchor(anchor)
	}
	s.chainTip, s.chainHeight, s.chainReady = tip, height, true
}
