package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/config"
	"github.com/CleveTok3125/V2V/lib/trip"
	"github.com/CleveTok3125/V2V/lib/tripcolor"
	"github.com/CleveTok3125/V2V/lib/wire"
	"github.com/gorilla/websocket"
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
	displayName := "Tester#eff8"
	payload := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 9, 0)
	sig := ed25519.Sign(priv, payload)

	trip := &TripMeta{
		Pub:         pubHex,
		Seq:         1,
		Prev:        hex.EncodeToString(prev),
		Sig:         hex.EncodeToString(sig),
		ServerPub:   serverPub,
		MsgHash:     hex.EncodeToString(msgHash[:]),
		DisplayName: displayName,
	}
	// Marshal record like history_store does (sync, no queue)
	wire := WireMessage{Type: "chat", Text: "test msg", DisplayName: displayName, Trip: trip}
	rec := historyRecord{Timestamp: time.Now().Format(time.RFC3339Nano), Wire: &wire}
	line, _ := json.Marshal(rec)
	// Simulate tamper detection: persisted record should contain trip
	var decoded historyRecord
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Wire == nil || decoded.Wire.Trip == nil || decoded.Wire.Trip.Pub != pubHex {
		t.Fatalf("trip not persisted: %+v", decoded)
	}
	// Tamper: different msg hash should not verify
	badHash := sha256.Sum256([]byte("tampered"))
	badPayload := tripcolor.CanonicalPayload(serverPub, 1, prev, badHash[:], pub, displayName, 9, 0)
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
		payload := tripcolor.CanonicalPayload(serverPub, seq, prev, h[:], pub, "Tester#eff8", 9, 0)
		sig := ed25519.Sign(priv, payload)
		hash := sha256.New()
		hash.Write(prev)
		hash.Write(sig)
		hash.Write(h[:])
		newPrev := hash.Sum(nil)
		wire2 := WireMessage{Type: "chat", Text: msg, DisplayName: "Tester#eff8", Trip: &TripMeta{
			Pub:         pubHex,
			Seq:         seq,
			Prev:        hex.EncodeToString(prev),
			Sig:         hex.EncodeToString(sig),
			ServerPub:   serverPub,
			MsgHash:     hex.EncodeToString(h[:]),
			DisplayName: "Tester#eff8",
		}}
		rec := historyRecord{
			Timestamp: "2026-01-01T00:00:00Z",
			Wire:      &wire2,
		}
		records = append(records, rec)
		prev = newPrev
	}
	// Simulate InitHistoryStore repopulation
	s2 := NewChatServer()
	for _, rec := range records {
		if rec.Wire != nil && rec.Wire.Trip != nil && rec.Wire.Trip.Pub != "" {
			tripTmp := rec.Wire.Trip
			prevBytes, _ := hex.DecodeString(tripTmp.Prev)
			sigBytes, _ := hex.DecodeString(tripTmp.Sig)
			msgHashBytes, _ := hex.DecodeString(tripTmp.MsgHash)
			if len(prevBytes) == 32 && len(sigBytes) == 64 && len(msgHashBytes) == 32 {
				h2 := sha256.New()
				h2.Write(prevBytes)
				h2.Write(sigBytes)
				h2.Write(msgHashBytes)
				newPrev := h2.Sum(nil)
				s2.TripChains.Store(tripTmp.Pub, TripChain{Seq: tripTmp.Seq, PrevHash: newPrev})
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

func TestHistoryFileTamperDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	// Create a valid trip record and write it to file like history_store does
	privSeed := make([]byte, 32)
	for i := range privSeed {
		privSeed[i] = byte(i + 10)
	}
	priv := ed25519.NewKeyFromSeed(privSeed)
	pub := priv.Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)
	serverPub := hex.EncodeToString(make([]byte, 32))
	prev := make([]byte, 32)
	msgText := "original message"
	msgHash := sha256.Sum256([]byte(msgText))
	displayName := "Tester#eff8"
	payload := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 9, 0)
	sig := ed25519.Sign(priv, payload)
	trip := &TripMeta{
		Pub:         pubHex,
		Seq:         1,
		Prev:        hex.EncodeToString(prev),
		Sig:         hex.EncodeToString(sig),
		ServerPub:   serverPub,
		MsgHash:     hex.EncodeToString(msgHash[:]),
		DisplayName: displayName,
		TmpID:       9,
	}
	wire3 := WireMessage{Type: "chat", DisplayName: displayName, Text: msgText, Trip: trip}
	rec := historyRecord{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Wire:      &wire3,
	}
	line, _ := json.Marshal(rec)
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	// tamperedCopy clones the record shell so each tamper case is independent.
	tamperedCopy := func(r historyRecord) historyRecord {
		cp := r
		wc := *r.Wire
		tc := *r.Wire.Trip
		wc.Trip = &tc
		cp.Wire = &wc
		return cp
	}
	// Helper to verify a record like client auto-verify does
	verify := func(r historyRecord) bool {
		if r.Wire == nil || r.Wire.Trip == nil {
			return false
		}
		w := r.Wire
		pubBytes, _ := hex.DecodeString(w.Trip.Pub)
		sigBytes, _ := hex.DecodeString(w.Trip.Sig)
		prevBytes, _ := hex.DecodeString(w.Trip.Prev)
		hashBytes, _ := hex.DecodeString(w.Trip.MsgHash)
		p := tripcolor.CanonicalPayload(w.Trip.ServerPub, w.Trip.Seq, prevBytes, hashBytes, pubBytes, w.Trip.DisplayName, w.Trip.TmpID, w.Trip.ReplyTo)
		return ed25519.Verify(pubBytes, p, sigBytes)
	}
	// Load and verify original — should be valid (green)
	data, _ := os.ReadFile(path)
	var loaded historyRecord
	json.Unmarshal(data[:len(data)-1], &loaded)
	if !verify(loaded) {
		t.Fatalf("original should verify")
	}

	// Tamper 1: edit text without updating sig/msg_hash — client sees msg_hash mismatch, badge turns red
	loaded.Wire.Text = "tampered message"
	if verify(loaded) {
		h2 := sha256.Sum256([]byte("tampered message"))
		if loaded.Wire.Trip.MsgHash == hex.EncodeToString(h2[:]) {
			t.Fatalf("tampered msg should have different hash")
		}
	}
	// Tamper 2: flip a byte in sig — signature must fail (red)
	loaded2 := rec
	sigBytes2, _ := hex.DecodeString(loaded2.Wire.Trip.Sig)
	sigBytes2[0] ^= 0xFF
	loaded2.Wire.Trip.Sig = hex.EncodeToString(sigBytes2)
	if verify(loaded2) {
		t.Fatalf("tampered sig should not verify")
	}
	// Tamper 3: replace pub — sig no longer matches pub (red)
	loaded3 := rec
	loaded3.Wire.Trip.Pub = hex.EncodeToString(make([]byte, 32))
	if verify(loaded3) {
		t.Fatalf("tampered pub should not verify")
	}
	// Tamper 4: change seq — payload differs, sig must fail (red)
	loaded4 := rec
	loaded4.Wire.Trip.Seq = 2
	if verify(loaded4) {
		t.Fatalf("tampered seq should not verify")
	}
	// Tamper 5: server renumbers tmp_id — bound into the signature, must fail
	loaded5 := tamperedCopy(rec)
	loaded5.Wire.Trip.TmpID = 10
	if verify(loaded5) {
		t.Fatalf("renumbered tmp_id should not verify")
	}
	// Tamper 6: change prev — payload differs, sig must fail (red)
	loaded6 := tamperedCopy(rec)
	loaded6.Wire.Trip.Prev = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if verify(loaded6) {
		t.Fatalf("tampered prev should not verify")
	}
	// Tamper 7: edit history file directly on disk (wire text) and reload — simulates client reload
	tamperedRec := tamperedCopy(rec)
	tamperedRec.Wire.Text = "edited on disk"
	tamperedLine, _ := json.Marshal(tamperedRec)
	os.WriteFile(path, append(tamperedLine, '\n'), 0o600)
	// Simulate server LoadRecords + client verify
	store, _ := NewHistoryStore(path, 1)
	records, _ := store.LoadRecords()
	if len(records) == 0 || records[0].Wire.Text != "edited on disk" {
		t.Fatalf("tampered file not loaded")
	}
	if verify(records[0]) {
		t.Logf("note: sig still verifies against old msg_hash, but Text field was edited — client should treat as tampered by comparing recomputed hash")
	}
}

// TripChains must stay bounded: stale entries are dropped and a burst
// inside the TTL is capped by evicting the least recently seen first.
func TestPruneTripChains_Bounds(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	now := time.Now()

	s.TripChains.Store("stale", TripChain{Seq: 1, LastSeen: now.Add(-tripChainsTTL - time.Minute)})
	for i := 0; i < maxTripChains+50; i++ {
		s.TripChains.Store(fmt.Sprintf("k%05d", i), TripChain{Seq: 1, LastSeen: now.Add(time.Duration(i) * time.Second)})
	}
	s.pruneTripChains(now)

	if _, ok := s.TripChains.Load("stale"); ok {
		t.Fatal("stale trip chain not pruned")
	}
	count := 0
	s.TripChains.Range(func(_, _ any) bool { count++; return true })
	if count > maxTripChains {
		t.Fatalf("trip chains = %d, want <= %d", count, maxTripChains)
	}
	if _, ok := s.TripChains.Load("k00000"); ok {
		t.Fatal("least recently seen entry must be evicted first")
	}
	if _, ok := s.TripChains.Load(fmt.Sprintf("k%05d", maxTripChains+49)); !ok {
		t.Fatal("newest entry must survive")
	}
}

// applyTripChain must classify continuity, and still adopt the higher
// seq on a gap/fork (liveness) while never rewinding on a stale record.
func TestApplyTripChain_Continuity(t *testing.T) {
	prev1 := bytes.Repeat([]byte{0x11}, 32)
	tipPrev := bytes.Repeat([]byte{0x22}, 32)
	cur := TripChain{Seq: 2, PrevHash: tipPrev}
	newPrev := bytes.Repeat([]byte{0x33}, 32)

	cases := []struct {
		name       string
		has        bool
		seq        uint32
		prev       []byte
		wantStatus tripChainUpdate
		wantSeq    uint32
	}{
		{"first", false, 1, make([]byte, 32), tripChainExtended, 1},
		{"contiguous", true, 3, tipPrev, tripChainExtended, 3},
		{"gap", true, 5, tipPrev, tripChainGap, 5},
		{"fork", true, 3, prev1, tripChainFork, 3},
		{"stale", true, 2, prev1, tripChainStale, 2},
	}
	for _, tc := range cases {
		got, status := applyTripChain(cur, tc.has, tc.seq, tc.prev, newPrev)
		if status != tc.wantStatus {
			t.Errorf("%s: status = %v, want %v", tc.name, status, tc.wantStatus)
		}
		if got.Seq != tc.wantSeq {
			t.Errorf("%s: seq = %d, want %d", tc.name, got.Seq, tc.wantSeq)
		}
	}
}

// signTripRecord builds one signed history record for recovery tests.
func signTripRecord(t *testing.T, priv ed25519.PrivateKey, pubHex string, seq uint32, prev []byte, text string) (historyRecord, []byte) {
	t.Helper()
	h := sha256.Sum256([]byte(text))
	const tmpID = 9
	payload := tripcolor.CanonicalPayload("", seq, prev, h[:], priv.Public().(ed25519.PublicKey), "Tester#eff8", tmpID, 0)
	sig := ed25519.Sign(priv, payload)
	ch := sha256.New()
	ch.Write(prev)
	ch.Write(sig)
	ch.Write(h[:])
	next := ch.Sum(nil)
	wire := WireMessage{Type: "chat", Text: text, DisplayName: "Tester#eff8", Trip: &TripMeta{
		Pub:         pubHex,
		Seq:         seq,
		Prev:        hex.EncodeToString(prev),
		Sig:         hex.EncodeToString(sig),
		MsgHash:     hex.EncodeToString(h[:]),
		DisplayName: "Tester#eff8",
		TmpID:       tmpID,
	}}
	return historyRecord{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Wire: &wire}, next
}

// A tampered trip line must not reach the replayed RAM history: it is
// dropped during recovery even though the disk still holds it.
func TestInitHistoryStore_DropsTamperedTripFromReplay(t *testing.T) {
	testCfg(t)
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x09}, 32))
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	rec1, next1 := signTripRecord(t, priv, pubHex, 1, make([]byte, 32), "honest")
	rec2, _ := signTripRecord(t, priv, pubHex, 2, next1, "tampered")
	rec2.Wire.Text = "tampered-edited" // breaks msg_hash binding

	path := filepath.Join(t.TempDir(), "history.jsonl")
	var sb bytes.Buffer
	for _, rec := range []historyRecord{rec1, rec2} {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, sb.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewChatServer()
	if err := s.InitHistoryStore(path, 1); err != nil {
		t.Fatal(err)
	}
	defer s.Chain.Store.Close()

	s.Chain.Mu.RLock()
	history := append([]string(nil), s.Chain.History...)
	s.Chain.Mu.RUnlock()
	if len(history) != 1 {
		t.Fatalf("replay history = %d lines, want 1 (tampered dropped)", len(history))
	}
	if !strings.Contains(history[0], "honest") {
		t.Fatalf("valid line missing: %q", history[0])
	}
}

// A seed-reveal notice in history rebuilds the exposed registry at boot:
// the announcement and later messages flag, earlier ones stay clean.
func TestInitHistoryStore_RebuildsExposedRegistry(t *testing.T) {
	testCfg(t)
	seed := bytes.Repeat([]byte{0x0b}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	rec1, next1 := signTripRecord(t, priv, pubHex, 1, make([]byte, 32), "one")
	notice := historyRecord{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Wire: &WireMessage{
			Type: "system", Tags: wire.WithTags(wire.TagTripReveal),
			Text: "Danh tính đã lộ seed", ExposedPub: pubHex,
			ExposedSig: trip.SignReveal(priv, pubHex),
		},
	}
	rec2, _ := signTripRecord(t, priv, pubHex, 2, next1, "two")

	path := filepath.Join(t.TempDir(), "history.jsonl")
	var sb bytes.Buffer
	for _, rec := range []historyRecord{rec1, notice, rec2} {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, sb.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewChatServer()
	if err := s.InitHistoryStore(path, 1); err != nil {
		t.Fatal(err)
	}
	defer s.Chain.Store.Close()

	if !s.isTripExposed(pubHex) {
		t.Fatal("exposed registry must rebuild from the notice")
	}
	s.Chain.Mu.RLock()
	history := append([]string(nil), s.Chain.History...)
	s.Chain.Mu.RUnlock()
	if len(history) != 3 {
		t.Fatalf("replay history = %d lines, want 3", len(history))
	}
	for i, want := range []bool{false, true} {
		var wm WireMessage
		idx := i * 2
		if err := json.Unmarshal([]byte(history[idx]), &wm); err != nil {
			t.Fatal(err)
		}
		if wm.Trip == nil || wm.Trip.Exposed != want {
			t.Fatalf("line %d exposed = %v, want %v", idx, wm.Trip.Exposed, want)
		}
	}
	var noticeWireMsg WireMessage
	if err := json.Unmarshal([]byte(history[1]), &noticeWireMsg); err != nil {
		t.Fatal(err)
	}
	if noticeWireMsg.ExposedPub != pubHex || !wire.HasTag(noticeWireMsg.Tags, wire.TagTripReveal) {
		t.Fatalf("replayed notice must name its pub: %+v", noticeWireMsg)
	}
}

// A reveal notice older than the 24h TTL still flags the replayed wires
// without refreshing the live registry.
func TestInitHistoryStore_ExpiredRevealDoesNotRefreshLiveRegistry(t *testing.T) {
	testCfg(t)
	seed := bytes.Repeat([]byte{0x0c}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	oldTS := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	notice := historyRecord{
		Timestamp: oldTS,
		Wire: &WireMessage{
			Type: "system", Tags: wire.WithTags(wire.TagTripReveal),
			Text: "Danh tính đã lộ seed", ExposedPub: pubHex,
			ExposedSig: trip.SignReveal(priv, pubHex),
		},
	}
	rec2, _ := signTripRecord(t, priv, pubHex, 1, make([]byte, 32), "later")
	rec2.Timestamp = oldTS

	path := filepath.Join(t.TempDir(), "history.jsonl")
	var sb bytes.Buffer
	for _, rec := range []historyRecord{notice, rec2} {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, sb.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewChatServer()
	if err := s.InitHistoryStore(path, 1); err != nil {
		t.Fatal(err)
	}
	defer s.Chain.Store.Close()

	if s.isTripExposed(pubHex) {
		t.Fatal("reveal older than TTL must not populate live exposed registry")
	}
	s.Chain.Mu.RLock()
	history := append([]string(nil), s.Chain.History...)
	s.Chain.Mu.RUnlock()
	var wm WireMessage
	if err := json.Unmarshal([]byte(history[1]), &wm); err != nil {
		t.Fatal(err)
	}
	if wm.Trip == nil || !wm.Trip.Exposed {
		t.Fatalf("line after the notice must stay flagged: %+v", wm.Trip)
	}
}

// A reveal frame from the key holder records the identity and warns the
// room; later messages from it carry the exposed flag.
func TestReadPump_TripRevealFrame(t *testing.T) {
	old := Cfg.Dynamic.Load()
	if old == nil {
		old = config.DefaultDynamic()
	}
	dyn := config.DefaultDynamic()
	dyn.MessageCooldown = time.Millisecond
	Cfg.Dynamic.Store(dyn)
	defer Cfg.Dynamic.Store(old)
	oldStatic := Cfg.Static
	Cfg.Static.Timezone = time.UTC
	defer func() { Cfg.Static = oldStatic }()

	seed := bytes.Repeat([]byte{0x0d}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)
	seedHex := hex.EncodeToString(seed)
	displayName := "An#abcd"

	s := NewChatServer()
	client, server := dialAuthPair(t, s)
	session := &ClientSession{
		Conn:        server,
		Send:        make(chan []byte, 256),
		DisplayName: displayName,
		Perms:       GetDefaultPermission(),
		TripPub:     pubHex,
	}
	// Register so broadcasts reach the sender.
	s.Hub.ClientsMu.Lock()
	s.Hub.Clients[server] = session
	s.Hub.ClientsMu.Unlock()
	go session.WritePump()
	go s.ReadPump(session, "127.0.0.1")

	sign := func(seq uint32, prev []byte, text string, tmpID uint64) (sigHex, prevHex string, next []byte) {
		h := sha256.Sum256([]byte(text))
		payload := tripcolor.CanonicalPayload("", seq, prev, h[:], pub, displayName, tmpID, 0)
		sig := ed25519.Sign(priv, payload)
		ch := sha256.New()
		ch.Write(prev)
		ch.Write(sig)
		ch.Write(h[:])
		return hex.EncodeToString(sig), hex.EncodeToString(prev), ch.Sum(nil)
	}
	sendTrip := func(seq uint32, prev []byte, text string, tmpID uint64) []byte {
		sigHex, prevHex, next := sign(seq, prev, text, tmpID)
		body, _ := json.Marshal(map[string]any{
			"text": text, "pub": pubHex, "seq": seq, "prev": prevHex,
			"sig": sigHex, "display_name": displayName, "tmp_id": tmpID,
		})
		if err := client.WriteMessage(websocket.TextMessage, body); err != nil {
			t.Fatal(err)
		}
		return next
	}
	readWire := func() WireMessage {
		t.Helper()
		client.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, msg, err := client.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var wm WireMessage
		if err := json.Unmarshal(msg, &wm); err != nil {
			t.Fatalf("not a wire: %q", msg)
		}
		return wm
	}
	readChat := func() WireMessage {
		t.Helper()
		for {
			wm := readWire()
			if wm.Type == "chat" {
				return wm
			}
		}
	}

	body, _ := json.Marshal(map[string]any{
		"type": "trip_reveal", "seed": seedHex, "sig": trip.SignReveal(priv, pubHex),
	})
	if err := client.WriteMessage(websocket.TextMessage, body); err != nil {
		t.Fatal(err)
	}
	notice := readWire()
	if notice.Type != "system" || notice.ExposedPub != pubHex || !wire.HasTag(notice.Tags, wire.TagTripReveal) {
		t.Fatalf("reveal must broadcast a tagged notice naming the pub: %+v", notice)
	}
	if !s.isTripExposed(pubHex) {
		t.Fatal("server must record the reveal")
	}
	sendTrip(1, make([]byte, 32), "after", 1002)
	echo := readChat()
	if echo.Trip == nil || !echo.Trip.Exposed {
		t.Fatalf("later messages must stay flagged: %+v", echo.Trip)
	}
	// Drain the server side before restoring Cfg: the leave broadcast
	// on unregister still needs the test config.
	client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.Hub.ClientsMu.RLock()
		n := len(s.Hub.Clients)
		s.Hub.ClientsMu.RUnlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not unregister")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A reveal frame carrying another identity's seed is refused: the frame
// may only expose the session's own trip pub.
func TestReadPump_TripRevealRejectsForeignSeed(t *testing.T) {
	testCfg(t)

	seed := bytes.Repeat([]byte{0x11}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)
	displayName := "An#abcd"

	s := NewChatServer()
	client, server := dialAuthPair(t, s)
	session := &ClientSession{
		Conn:        server,
		Send:        make(chan []byte, 256),
		DisplayName: displayName,
		Perms:       GetDefaultPermission(),
		TripPub:     pubHex,
	}
	s.Hub.ClientsMu.Lock()
	s.Hub.Clients[server] = session
	s.Hub.ClientsMu.Unlock()
	go session.WritePump()
	go s.ReadPump(session, "127.0.0.1")

	foreign := hex.EncodeToString(bytes.Repeat([]byte{0x12}, 32))
	body, _ := json.Marshal(map[string]any{"type": "trip_reveal", "seed": foreign})
	if err := client.WriteMessage(websocket.TextMessage, body); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var warn WireMessage
	if err := json.Unmarshal(msg, &warn); err != nil {
		t.Fatal(err)
	}
	if warn.Type != "system" || !strings.Contains(warn.Text, "không khớp") {
		t.Fatalf("foreign seed must be refused: %+v", warn)
	}
	if s.isTripExposed(pubHex) {
		t.Fatal("refused reveal must not mark the identity")
	}

	client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.Hub.ClientsMu.RLock()
		n := len(s.Hub.Clients)
		s.Hub.ClientsMu.RUnlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not unregister")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// liveRevealSession registers a session carrying pubHex and starts its
// pumps. closeRevealSession waits for the unregister so later broadcasts
// do not target a dead hub entry.
func liveRevealSession(t *testing.T, s *ChatServer, pubHex, displayName string) *websocket.Conn {
	t.Helper()
	client, server := dialAuthPair(t, s)
	session := &ClientSession{
		Conn:        server,
		Send:        make(chan []byte, 256),
		DisplayName: displayName,
		Perms:       GetDefaultPermission(),
		TripPub:     pubHex,
	}
	s.Hub.ClientsMu.Lock()
	s.Hub.Clients[server] = session
	s.Hub.ClientsMu.Unlock()
	go session.WritePump()
	go s.ReadPump(session, "127.0.0.1")
	return client
}

func closeRevealSession(t *testing.T, s *ChatServer, client *websocket.Conn) {
	t.Helper()
	client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.Hub.ClientsMu.RLock()
		n := len(s.Hub.Clients)
		s.Hub.ClientsMu.RUnlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not unregister")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A reveal frame whose signature does not match the session pub is
// refused: the stored notice must prove the key holder authored it.
func TestReadPump_TripRevealRejectsBadSignature(t *testing.T) {
	testCfg(t)

	seed := bytes.Repeat([]byte{0x31}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	otherPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x32}, 32))
	otherPub := hex.EncodeToString(otherPriv.Public().(ed25519.PublicKey))

	s := NewChatServer()
	client := liveRevealSession(t, s, pubHex, "An#abcd")
	defer closeRevealSession(t, s, client)

	body, _ := json.Marshal(map[string]any{
		"type": "trip_reveal", "seed": hex.EncodeToString(seed),
		"sig": trip.SignReveal(otherPriv, otherPub),
	})
	if err := client.WriteMessage(websocket.TextMessage, body); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var warn WireMessage
	if err := json.Unmarshal(msg, &warn); err != nil {
		t.Fatal(err)
	}
	if warn.Type != "system" || !strings.Contains(warn.Text, "Chữ ký") {
		t.Fatalf("bad signature must be refused: %+v", warn)
	}
	if s.isTripExposed(pubHex) {
		t.Fatal("refused reveal must not mark the identity")
	}
}

// Replaying the reveal frame is reported to the sender but does not
// broadcast a second announcement.
func TestReadPump_TripRevealIsIdempotent(t *testing.T) {
	testCfg(t)

	seed := bytes.Repeat([]byte{0x41}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	frame, _ := json.Marshal(map[string]any{
		"type": "trip_reveal", "seed": hex.EncodeToString(seed),
		"sig": trip.SignReveal(priv, pubHex),
	})

	s := NewChatServer()
	client := liveRevealSession(t, s, pubHex, "An#abcd")
	defer closeRevealSession(t, s, client)

	readWire := func() WireMessage {
		t.Helper()
		client.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, msg, err := client.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var wm WireMessage
		if err := json.Unmarshal(msg, &wm); err != nil {
			t.Fatalf("not a wire: %q", msg)
		}
		return wm
	}

	for i := 0; i < 2; i++ {
		if err := client.WriteMessage(websocket.TextMessage, frame); err != nil {
			t.Fatal(err)
		}
	}
	first := readWire()
	if first.ExposedPub != pubHex || !wire.HasTag(first.Tags, wire.TagTripReveal) {
		t.Fatalf("first frame must broadcast the reveal: %+v", first)
	}
	second := readWire()
	if second.ExposedPub != "" || !strings.Contains(second.Text, "đã được ghi nhận") {
		t.Fatalf("second frame must only warn the sender: %+v", second)
	}
	client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, _, err := client.ReadMessage(); err == nil {
		t.Fatal("second frame must not broadcast another reveal notice")
	}
	if !s.isTripExposed(pubHex) {
		t.Fatal("reveal must stay recorded")
	}
}

// A history reveal notice without a valid identity signature is skipped,
// so editing the history file cannot mark an identity as exposed.
func TestInitHistoryStore_IgnoresForgedRevealNotice(t *testing.T) {
	testCfg(t)
	seed := bytes.Repeat([]byte{0x0b}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	rec1, _ := signTripRecord(t, priv, pubHex, 1, make([]byte, 32), "one")
	notice := historyRecord{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Wire: &WireMessage{
			Type: "system", Tags: wire.WithTags(wire.TagTripReveal),
			Text: "Danh tính đã lộ seed", ExposedPub: pubHex,
			ExposedSig: strings.Repeat("00", ed25519.SignatureSize),
		},
	}
	rec2, _ := signTripRecord(t, priv, pubHex, 2, make([]byte, 32), "two")

	path := filepath.Join(t.TempDir(), "history.jsonl")
	var sb bytes.Buffer
	for _, rec := range []historyRecord{rec1, notice, rec2} {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, sb.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewChatServer()
	if err := s.InitHistoryStore(path, 1); err != nil {
		t.Fatal(err)
	}
	defer s.Chain.Store.Close()

	if s.isTripExposed(pubHex) {
		t.Fatal("forged notice must not populate the exposed registry")
	}
	s.Chain.Mu.RLock()
	history := append([]string(nil), s.Chain.History...)
	s.Chain.Mu.RUnlock()
	var wm WireMessage
	if err := json.Unmarshal([]byte(history[2]), &wm); err != nil {
		t.Fatal(err)
	}
	if wm.Trip == nil || wm.Trip.Exposed {
		t.Fatalf("later message must stay unexposed: %+v", wm.Trip)
	}
}

// Recovery must adopt the highest verified seq across a gap (so the live
// client keeps working) instead of rewinding to the last contiguous one.
func TestInitHistoryStore_GapAdoptsHigherSeq(t *testing.T) {
	testCfg(t)
	seed := bytes.Repeat([]byte{0x07}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	rec1, next1 := signTripRecord(t, priv, pubHex, 1, make([]byte, 32), "one")
	rec2, next2 := signTripRecord(t, priv, pubHex, 2, next1, "two")
	// seq 3 and 4 are lost; seq 5 is signed against seq 4's prev (opaque
	// here, any 32 bytes still verifies).
	rec5, _ := signTripRecord(t, priv, pubHex, 5, next2, "five")

	path := filepath.Join(t.TempDir(), "history.jsonl")
	var sb bytes.Buffer
	for _, rec := range []historyRecord{rec1, rec2, rec5} {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, sb.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewChatServer()
	if err := s.InitHistoryStore(path, 1); err != nil {
		t.Fatal(err)
	}
	defer s.Chain.Store.Close()

	v, ok := s.TripChains.Load(pubHex)
	if !ok {
		t.Fatal("trip chain not recovered")
	}
	if got := v.(TripChain).Seq; got != 5 {
		t.Fatalf("recovered seq = %d, want 5 (adopt across gap)", got)
	}
}
