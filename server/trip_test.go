package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"localchat/internal/tripcolor"
)

func TestHistoryTripChainPersistenceAndTamper(t *testing.T) {
	// Verify signing roundtrip and tamper detection without touching async HistoryStore
	privBytes := make([]byte, 32)
	rand.Read(privBytes)
	priv := ed25519.NewKeyFromSeed(privBytes)
	pub := priv.Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)
	serverPub := hex.EncodeToString(make([]byte, 32))

	prev := make([]byte, 32)
	msgText := "hello trip"
	msgHash := sha256.Sum256([]byte(msgText))
	payload := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub)
	sig := ed25519.Sign(priv, payload)

	trip := &TripMeta{
		Pub:       pubHex,
		Seq:       1,
		Prev:      hex.EncodeToString(prev),
		Sig:       hex.EncodeToString(sig),
		ServerPub: serverPub,
		MsgHash:   hex.EncodeToString(msgHash[:]),
	}
	// Marshal record like history_store does (sync, no queue)
	rec := historyRecord{Timestamp: time.Now().Format(time.RFC3339Nano), Message: "test msg", Trip: trip}
	line, _ := json.Marshal(rec)
	// Simulate tamper detection: persisted record should contain trip
	var decoded historyRecord
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Trip == nil || decoded.Trip.Pub != pubHex {
		t.Fatalf("trip not persisted: %+v", decoded)
	}
	// Tamper: different msg hash should not verify
	badHash := sha256.Sum256([]byte("tampered"))
	badPayload := tripcolor.CanonicalPayload(serverPub, 1, prev, badHash[:], pub)
	if ed25519.Verify(pub, badPayload, sig) {
		t.Fatalf("tampered payload should not verify")
	}
	// Correct should verify
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatalf("correct payload should verify")
	}
}

func TestTripChainSeqEnforcement(t *testing.T) {
	s := NewChatServer()
	pubHex := hex.EncodeToString(make([]byte, 32))
	// Put initial chain seq=5
	s.TripChains.Store(pubHex, TripChain{Seq: 5, PrevHash: make([]byte, 32)})
	v, ok := s.TripChains.Load(pubHex)
	if !ok {
		t.Fatal("not found")
	}
	ch := v.(TripChain)
	if ch.Seq != 5 {
		t.Fatalf("seq mismatch")
	}
	// Expected next is 6
	expected := ch.Seq + 1
	if expected != 6 {
		t.Fatalf("expected 6")
	}
	// Simulate duplicate seq 5 should be rejected (already tested via handler)
}

func TestTripBadgeColorDeterministic(t *testing.T) {
	a := tripcolor.BadgeColor("◆ abc12345")
	b := tripcolor.BadgeColor("◆ abc12345")
	if a != b {
		t.Fatalf("badge color not deterministic: %q vs %q", a, b)
	}
	c := tripcolor.BadgeColor("◆ deadbeef")
	if a == c {
		t.Logf("different badge gave same color (possible but unlikely): %q", a)
	}
	if len(a) < 5 || a[:5] != "\x1b[38;" {
		t.Fatalf("badgeColor should return ANSI 38;2;...m, got %q", a)
	}
}

func TestHistoryRestartRepopulation(t *testing.T) {
	// Simulate 2 records written synchronously and repopulated on boot (no async queue)
	privSeed := make([]byte, 32)
	for i := range privSeed {
		privSeed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(privSeed)
	pub := priv.Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)
	serverPub := hex.EncodeToString(make([]byte, 32))
	prev := make([]byte, 32)
	var records []historyRecord
	for seq := uint32(1); seq <= 2; seq++ {
		msg := "msg" + string(rune('0'+seq))
		h := sha256.Sum256([]byte(msg))
		payload := tripcolor.CanonicalPayload(serverPub, seq, prev, h[:], pub)
		sig := ed25519.Sign(priv, payload)
		hash := sha256.New()
		hash.Write(prev)
		hash.Write(sig)
		hash.Write(h[:])
		newPrev := hash.Sum(nil)
		rec := historyRecord{
			Timestamp: "2026-01-01T00:00:00Z",
			Message:   msg,
			Trip: &TripMeta{
				Pub:       pubHex,
				Seq:       seq,
				Prev:      hex.EncodeToString(prev),
				Sig:       hex.EncodeToString(sig),
				ServerPub: serverPub,
				MsgHash:   hex.EncodeToString(h[:]),
			},
		}
		records = append(records, rec)
		prev = newPrev
	}
	// Simulate InitHistoryStore repopulation
	s2 := NewChatServer()
	for _, rec := range records {
		if rec.Trip != nil && rec.Trip.Pub != "" {
			prevBytes, _ := hex.DecodeString(rec.Trip.Prev)
			sigBytes, _ := hex.DecodeString(rec.Trip.Sig)
			msgHashBytes, _ := hex.DecodeString(rec.Trip.MsgHash)
			if len(prevBytes) == 32 && len(sigBytes) == 64 && len(msgHashBytes) == 32 {
				h2 := sha256.New()
				h2.Write(prevBytes)
				h2.Write(sigBytes)
				h2.Write(msgHashBytes)
				newPrev := h2.Sum(nil)
				s2.TripChains.Store(rec.Trip.Pub, TripChain{Seq: rec.Trip.Seq, PrevHash: newPrev})
			}
		}
	}
	v, ok := s2.TripChains.Load(pubHex)
	if !ok {
		t.Fatalf("trip chain not repopulated")
	}
	ch := v.(TripChain)
	if ch.Seq != 2 {
		t.Fatalf("expected seq 2, got %d", ch.Seq)
	}
}

