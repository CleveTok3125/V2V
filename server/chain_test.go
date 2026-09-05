package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/CleveTok3125/V2V/internal/chain"
)

func testChainCfg() func() {
	Cfg.Dynamic.Store(&DynamicConfig{MaxHistoryBytes: 1 << 20})
	return func() { Cfg.Dynamic.Store(nil) }
}

func verifyStoredChain(t *testing.T, s *ChatServer) {
	t.Helper()
	if err := checkStoredChain(s); err != nil {
		t.Fatal(err)
	}
}

// checkStoredChain replays the stored log and reports the first broken
// link.
func checkStoredChain(s *ChatServer) error {
	// Test servers carry no identity, so the chain starts at Genesis("").
	return checkStoredChainFrom(s, chain.Genesis(""))
}

func checkStoredChainFrom(s *ChatServer, prev [32]byte) error {
	var anchor string
	var anchored bool
	var wantHeight uint64
	for i, msgStr := range s.ChatHistory {
		var wire WireMessage
		if err := json.Unmarshal([]byte(msgStr), &wire); err != nil || wire.ChainHash == "" {
			anchor, anchored = msgStr, true
			continue
		}
		if anchored {
			prev, anchored = chain.LegacyAnchor(anchor), false
		}
		wantHeight++
		gotPrev, ok := chain.ParseHex64(wire.ChainPrev)
		if !ok || gotPrev != prev {
			return fmt.Errorf("msg %d: broken prev link", i)
		}
		if wire.ChainHeight != wantHeight {
			return fmt.Errorf("msg %d: height %d, want %d", i, wire.ChainHeight, wantHeight)
		}
		want, ok := chain.ParseHex64(wire.ChainHash)
		if !ok || !chain.VerifyLink(gotPrev, wire.ChainHeight, wire.TmpID, wire.Type, wire.Time, wire.DisplayName, wire.Text, tripSigOf(wire), want) {
			return fmt.Errorf("msg %d: broken hash link", i)
		}
		prev = want
	}
	return nil
}

func TestChainLinkSequence(t *testing.T) {
	defer testChainCfg()()
	s := NewChatServer()
	var last WireMessage
	for i := 1; i <= 3; i++ {
		last = s.linkAndStore(WireMessage{Type: "chat", Time: "15:04", DisplayName: "Alice", Text: fmt.Sprintf("msg %d", i), TmpID: uint64(i)})
		if last.ChainHeight != uint64(i) {
			t.Fatalf("height = %d, want %d", last.ChainHeight, i)
		}
		if last.ChainHash == "" || last.ChainPrev == "" {
			t.Fatal("link fields must be set")
		}
	}
	verifyStoredChain(t, s)
	// Server never assigns tmp_id: the relayed value is the sender's.
	if last.TmpID != 3 {
		t.Fatalf("tmp_id = %d, want sender value 3", last.TmpID)
	}
}

func TestChainTamperBreaksLink(t *testing.T) {
	defer testChainCfg()()
	s := NewChatServer()
	s.linkAndStore(WireMessage{Type: "chat", Time: "15:04", DisplayName: "Alice", Text: "original", TmpID: 1})
	s.linkAndStore(WireMessage{Type: "chat", Time: "15:05", DisplayName: "Bob", Text: "reply", TmpID: 1})
	// Server-side edit of stored text breaks its own link and every later one.
	var first WireMessage
	if err := json.Unmarshal([]byte(s.ChatHistory[0]), &first); err != nil {
		t.Fatal(err)
	}
	first.Text = "edited by server"
	bad, _ := json.Marshal(first)
	s.ChatHistory[0] = string(bad)
	if err := checkStoredChain(s); err == nil {
		t.Fatal("tampered chain must fail verification")
	}
}

func TestChainResumeAfterRestart(t *testing.T) {
	defer testChainCfg()()
	s := NewChatServer()
	s.ChatHistory = append(s.ChatHistory, "legacy raw line without chain")
	w1 := s.linkAndStore(WireMessage{Type: "chat", Time: "15:04", DisplayName: "A", Text: "one", TmpID: 1})
	w2 := s.linkAndStore(WireMessage{Type: "chat", Time: "15:05", DisplayName: "B", Text: "two", TmpID: 7})
	if w1.ChainHeight != 1 {
		t.Fatalf("first height = %d, want 1", w1.ChainHeight)
	}
	// Fresh server over the same history must resume, not fork.
	r := NewChatServer()
	r.ChatHistory = append([]string{}, s.ChatHistory...)
	r.HistoryMu.Lock()
	r.initChainLocked()
	tip, height := r.chainTip, r.chainHeight
	r.HistoryMu.Unlock()
	wantTip, _ := chain.ParseHex64(w2.ChainHash)
	if tip != wantTip || height != 2 {
		t.Fatalf("resume tip/height = %x/%d, want %s/2", tip, height, w2.ChainHash)
	}
	// Next message continues the chain.
	r.BroadcastMu.Lock()
	w3 := r.linkAndStore(WireMessage{Type: "chat", Time: "15:06", DisplayName: "C", Text: "three", TmpID: 1})
	r.BroadcastMu.Unlock()
	if w3.ChainHeight != 3 {
		t.Fatalf("post-restart height = %d, want 3", w3.ChainHeight)
	}
	verifyStoredChain(t, r)
}

func TestChainConcurrentAppend(t *testing.T) {
	defer testChainCfg()()
	s := NewChatServer()
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.BroadcastMu.Lock()
			s.linkAndStore(WireMessage{Type: "chat", Time: "15:04", DisplayName: "U", Text: fmt.Sprintf("m%d", i), TmpID: uint64(i + 1)})
			s.BroadcastMu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(s.ChatHistory) != n {
		t.Fatalf("stored %d, want %d", len(s.ChatHistory), n)
	}
	// Heights must be a permutation of 1..n: no reuse, no gap.
	seen := make([]bool, n+1)
	for _, msgStr := range s.ChatHistory {
		var wire WireMessage
		if err := json.Unmarshal([]byte(msgStr), &wire); err != nil {
			t.Fatal(err)
		}
		if wire.ChainHeight < 1 || wire.ChainHeight > n || seen[wire.ChainHeight] {
			t.Fatalf("duplicate or out-of-range height %d", wire.ChainHeight)
		}
		seen[wire.ChainHeight] = true
	}
	// In height order the links verify end to end.
	ordered := append([]string{}, s.ChatHistory...)
	sort.Slice(ordered, func(i, j int) bool {
		var a, b WireMessage
		json.Unmarshal([]byte(ordered[i]), &a)
		json.Unmarshal([]byte(ordered[j]), &b)
		return a.ChainHeight < b.ChainHeight
	})
	prev := chain.Genesis("")
	for _, msgStr := range ordered {
		var wire WireMessage
		json.Unmarshal([]byte(msgStr), &wire)
		gotPrev, _ := chain.ParseHex64(wire.ChainPrev)
		want, _ := chain.ParseHex64(wire.ChainHash)
		if gotPrev != prev || !chain.VerifyLink(gotPrev, wire.ChainHeight, wire.TmpID, wire.Type, wire.Time, wire.DisplayName, wire.Text, tripSigOf(wire), want) {
			t.Fatalf("height %d: broken link", wire.ChainHeight)
		}
		prev = want
	}
}
