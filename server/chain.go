package main

import (
	"encoding/hex"
	"encoding/json"
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

// linkAndStore chains one wire message, stores it in memory + disk, and
// returns the chained copy for broadcast. Callers must hold BroadcastMu;
// this takes HistoryMu (Chain.Mu) internally (leaf lock, never the reverse
// order).
func (c *ChainService) linkAndStore(wire WireMessage, serverPub string) WireMessage {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if !c.ready {
		c.initChainLocked(serverPub)
	}
	c.height++
	prev := c.tip
	h := chain.Hash(prev, c.height, wire.TmpID, wire.ReplyTo, wire.Type, wire.Time, wire.DisplayName, wire.Text, chain.TripSigOf(wire))
	wire.ChainPrev = hex.EncodeToString(prev[:])
	wire.ChainHash = hex.EncodeToString(h[:])
	wire.ChainHeight = c.height
	wire.ChainVer = chainVersion
	c.tip = h
	data, _ := json.Marshal(wire)
	c.appendMessageLocked(string(data))
	if c.Store != nil {
		c.Store.EnqueueWire(wire, time.Now().In(Cfg.Static.Timezone))
	}
	return wire
}

// initChainLocked resumes the tip from stored history or starts a new
// chain. Genesis derives from the server identity, so restarts resume the
// same chain without extra state. Pre-chain legacy records are anchored,
// never rewritten: the first chained message links to an anchor over the
// last legacy line. Caller must hold HistoryMu (Chain.Mu).
func (c *ChainService) initChainLocked(serverPub string) {
	tip := chain.Genesis(serverPub)
	var height uint64
	var anchor string
	var anchored bool
	broken := false
	for _, msgStr := range c.History {
		var wire WireMessage
		if err := json.Unmarshal([]byte(msgStr), &wire); err != nil {
			// Legacy record: remember as anchor candidate, keep scanning.
			anchor = msgStr
			anchored = true
			continue
		}
		if wire.Type == "system" && wire.ChainHash == "" {
			// Unchained notification (join/leave/date): never chain,
			// never anchor. Skipping here is what keeps a burst of
			// visits from hijacking the resume anchor.
			continue
		}
		if wire.ChainHash == "" {
			// Legacy chat record: remember as anchor candidate.
			anchor = msgStr
			anchored = true
			continue
		}
		prev, ok1 := chain.ParseHex64(wire.ChainPrev)
		want, ok2 := chain.ParseHex64(wire.ChainHash)
		if !ok1 || !ok2 {
			broken = true
			// Drop the anchor: a malformed line between the legacy tail
			// and this record makes the cached anchor stale.
			anchored = false
			logWarnf("⛔ [CHAIN TAMPER] height %d: malformed link fields", wire.ChainHeight)
			continue
		}
		expectPrev := tip
		if anchored {
			expectPrev = chain.LegacyAnchor(anchor)
			anchored = false
		}
		if prev != expectPrev || wire.ChainHeight != height+1 {
			broken = true
			logWarnf("⛔ [CHAIN TAMPER] height %d: link break, adopting tip anyway (chat stays up; clients holding older tips flag the fork)", wire.ChainHeight)
			tip, height = want, wire.ChainHeight
			continue
		}
		if !chain.VerifyWire(prev, wire, want) {
			broken = true
			logWarnf("⛔ [CHAIN TAMPER] height %d: link break, adopting tip anyway (chat stays up; clients holding older tips flag the fork)", wire.ChainHeight)
		}
		tip, height = want, wire.ChainHeight
	}
	if broken {
		logWarnf("⛔ [CHAIN TAMPER] history failed verification on load; tip adopted, online clients detect forks via persisted tips")
	}
	if anchored && height == 0 {
		// Legacy-only log: the next message anchors to the last legacy
		// line instead of genesis.
		tip = chain.LegacyAnchor(anchor)
	}
	c.tip, c.height, c.ready = tip, height, true
}

// serverPub returns the hex identity for chain genesis (empty in tests).
func (s *ChatServer) serverPub() string {
	if s.ServerID != nil {
		return s.ServerID.PublicKey
	}
	return ""
}
