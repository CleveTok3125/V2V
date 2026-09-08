package wire

import (
	"encoding/json"
	"testing"
)

// TestWireJSONKeySet pins the exact serialized contract both binaries
// share. JSON silently drops unknown fields, so any added/renamed key
// must be a conscious protocol change reviewed here first.
func TestWireJSONKeySet(t *testing.T) {
	full := WireMessage{
		Type: "system", Time: "12:00", DisplayName: "Bob#1234", SysKind: "join", Text: "hi",
		Trip:        &TripMeta{Pub: "p", Seq: 1, Prev: "q", Sig: "s", ServerPub: "sp", MsgHash: "m", DisplayName: "Bob#1234", TmpID: 2, ReplyTo: 3},
		TmpID:       2,
		ReplyTo:     3,
		ChainPrev:   "cp",
		ChainHash:   "ch",
		ChainHeight: 4,
		ChainVer:    2,
	}
	raw, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	wantTop := []string{"type", "time", "displayName", "sys_kind", "text", "trip", "tmp_id", "reply_to", "chain_prev", "chain_hash", "chain_height", "chain_ver"}
	if len(got) != len(wantTop) {
		t.Fatalf("top-level keys = %v, want %v", keysOf(got), wantTop)
	}
	for _, k := range wantTop {
		if _, ok := got[k]; !ok {
			t.Errorf("missing top-level key %q in %s", k, raw)
		}
	}
	trip, ok := got["trip"].(map[string]any)
	if !ok {
		t.Fatalf("trip not an object in %s", raw)
	}
	wantTrip := []string{"pub", "seq", "prev", "sig", "server_pub", "msg_hash", "display_name", "tmp_id", "reply_to"}
	if len(trip) != len(wantTrip) {
		t.Fatalf("trip keys = %v, want %v", keysOf(trip), wantTrip)
	}
	for _, k := range wantTrip {
		if _, ok := trip[k]; !ok {
			t.Errorf("missing trip key %q in %s", k, raw)
		}
	}

	// Legacy payloads (pre-union fields absent) must still decode.
	var legacy WireMessage
	if err := json.Unmarshal([]byte(`{"type":"chat","text":"old","trip":{"pub":"p","seq":1,"prev":"q","sig":"s","server_pub":"sp"}}`), &legacy); err != nil {
		t.Fatalf("legacy decode failed: %v", err)
	}
	if legacy.Trip == nil || legacy.Trip.Pub != "p" {
		t.Fatalf("legacy trip lost: %+v", legacy.Trip)
	}

	// AuthPacket must never serialize IdentityPub.
	ap := AuthPacket{Type: "auth", IdentityPub: "secret-server-side"}
	raw, _ = json.Marshal(ap)
	var amap map[string]any
	_ = json.Unmarshal(raw, &amap)
	if _, ok := amap["IdentityPub"]; ok {
		t.Error("IdentityPub leaked onto the wire")
	}

	// HistoryJoins defaults off: absent means filtered replay.
	var bare AuthPacket
	if err := json.Unmarshal([]byte(`{"type":"auth"}`), &bare); err != nil || bare.HistoryJoins {
		t.Fatalf("bare auth must decode with HistoryJoins=false: %+v %v", bare, err)
	}

	// HistorySync trailer pins its key set as well.
	sync := HistorySync{Type: "history_sync", MinHeight: 1, MaxHeight: 142, Sent: 32, Total: 142,
		OmittedHashes: []string{"aa", "bb"}, Truncated: true}
	raw, _ = json.Marshal(sync)
	var smap map[string]any
	_ = json.Unmarshal(raw, &smap)
	for _, k := range []string{"type", "min_height", "max_height", "sent", "total", "omitted_hashes", "truncated"} {
		if _, ok := smap[k]; !ok {
			t.Errorf("missing history_sync key %q in %s", k, raw)
		}
	}

	// SysKind round-trips on system lines.
	var sys WireMessage
	if err := json.Unmarshal([]byte(`{"type":"system","sys_kind":"join","text":"x"}`), &sys); err != nil || sys.SysKind != "join" {
		t.Fatalf("sys_kind lost: %+v %v", sys, err)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
