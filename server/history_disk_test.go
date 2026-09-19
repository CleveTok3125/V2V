package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/CleveTok3125/V2V/internal/config"
)

// heightsOf extracts chained heights from stored lines in order,
// skipping unchained lines.
func heightsOf(t *testing.T, lines []string) []uint64 {
	t.Helper()
	var out []uint64
	for _, l := range lines {
		var wire WireMessage
		if err := json.Unmarshal([]byte(l), &wire); err != nil || wire.ChainHeight == 0 {
			continue
		}
		out = append(out, wire.ChainHeight)
	}
	return out
}

func TestSelectWindow(t *testing.T) {
	lines := []string{
		noticeLine("date", "d1"),
		tagLine(1, "chat", "", "one"),
		tagLine(2, "chat", "", "two"),
		noticeLine("date", "d2"),
		tagLine(3, "chat", "", "three"),
	}
	if got := heightsOf(t, selectWindow(lines, 3, 10)); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("before 3 = %v, want [1 2]", got)
	}
	if got := selectWindow(lines, 3, 10); len(got) != 4 {
		t.Fatalf("before 3 keeps the interleaved notice, got %d lines", len(got))
	}
	if got := selectWindow(lines, 0, 2); len(got) != 2 {
		t.Fatalf("before 0 limit 2 = %d lines, want the tail [d2,#3]", len(got))
	}
	if got := heightsOf(t, selectWindow(lines, 0, 3)); !equalHeights(got, []uint64{2, 3}) {
		t.Fatalf("before 0 limit 3 = %v, want tail [2 3]", got)
	}
	if got := selectWindow(lines, 1, 10); len(got) != 1 {
		t.Fatalf("before 1 = %d lines, want just the leading notice", len(got))
	}
	if got := selectWindow(lines, 9, 10); len(got) != 5 {
		t.Fatalf("before past every height means the tail window, got %d lines", len(got))
	}
	if got := selectWindow(lines, 3, 0); got != nil {
		t.Fatalf("limit 0 must be nil, got %q", got)
	}
}

func TestSeekOffset(t *testing.T) {
	samples := []heightSample{{height: 10, offset: 100}, {height: 20, offset: 200}, {height: 30, offset: 300}}
	if off := seekOffset(samples, 5); off != 0 {
		t.Fatalf("below first sample = %d, want 0", off)
	}
	if off := seekOffset(samples, 10); off != 0 {
		t.Fatalf("at first sample height must start at 0, got %d", off)
	}
	if off := seekOffset(samples, 25); off != 200 {
		t.Fatalf("between samples = %d, want 200", off)
	}
	if off := seekOffset(samples, 99); off != 300 {
		t.Fatalf("past last sample = %d, want 300", off)
	}
	if off := seekOffset(nil, 99); off != 0 {
		t.Fatalf("no samples = %d, want 0", off)
	}
}

func writeTestZst(t *testing.T, path string, lines []string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if _, err := enc.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTestLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedDiskHistory writes archive heights 1-2 plus active heights 5-6.
// A leftover .old is intentionally absent: the loader skips .old when
// .old.zst exists, and the index must mirror exactly that.
func seedDiskHistory(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "history.jsonl")
	writeTestZst(t, path+".old.zst", []string{
		string(mustRecord(t, tagLine(1, "chat", "", "one"))),
		string(mustRecord(t, tagLine(2, "chat", "", "two"))),
	})
	writeTestLines(t, path, []string{
		string(mustRecord(t, tagLine(5, "chat", "", "five"))),
		string(mustRecord(t, tagLine(6, "chat", "", "six"))),
	})
	return path
}

// seedRawHistory writes a raw .old (heights 3-4, no archive) plus
// active heights 5-6.
func seedRawHistory(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "history.jsonl")
	writeTestLines(t, path+".old", []string{
		string(mustRecord(t, tagLine(3, "chat", "", "three"))),
		string(mustRecord(t, tagLine(4, "chat", "", "four"))),
	})
	writeTestLines(t, path, []string{
		string(mustRecord(t, tagLine(5, "chat", "", "five"))),
		string(mustRecord(t, tagLine(6, "chat", "", "six"))),
	})
	return path
}

// mustRecord wraps one stored line in its on-disk record envelope.
func mustRecord(t *testing.T, stored string) []byte {
	t.Helper()
	var wire WireMessage
	if err := json.Unmarshal([]byte(stored), &wire); err == nil && wire.Type != "" {
		data, _ := json.Marshal(historyRecord{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Wire: &wire})
		return data
	}
	data, _ := json.Marshal(historyRecord{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Message: stored})
	return data
}

func TestIndexBootTiers(t *testing.T) {
	dir := t.TempDir()
	path := seedDiskHistory(t, dir)
	store, err := NewHistoryStore(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadRecords(); err != nil {
		t.Fatal(err)
	}
	if len(store.idx) != 2 {
		t.Fatalf("idx gens = %d, want 2 (leftover .old absent here)", len(store.idx))
	}
	wantTiers := []int{DiskLookupArchive, DiskLookupActive}
	wantBounds := [][2]uint64{{1, 2}, {5, 6}}
	for i, g := range store.idx {
		if g.tier != wantTiers[i] {
			t.Fatalf("gen %d tier = %d, want %d", i, g.tier, wantTiers[i])
		}
		if !g.chained || g.minH != wantBounds[i][0] || g.maxH != wantBounds[i][1] {
			t.Fatalf("gen %d bounds = %v/%d-%d, want %v", i, g.chained, g.minH, g.maxH, wantBounds[i])
		}
	}
	if len(store.idx[0].samples) != 0 {
		t.Fatal("archive must keep bounds only, no samples")
	}
	if len(store.idx[1].samples) == 0 {
		t.Fatal("active raw generation must carry samples")
	}
}

// TestIndexBootRawLeftover pins the loader's skip rule in the index: a
// leftover .old next to a good .old.zst must not contribute records,
// so no raw entry may exist for it.
func TestIndexBootRawLeftover(t *testing.T) {
	dir := t.TempDir()
	path := seedDiskHistory(t, dir)
	writeTestLines(t, path+".old", []string{
		string(mustRecord(t, tagLine(3, "chat", "", "three"))),
	})
	store, err := NewHistoryStore(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recs, err := store.LoadRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("records = %d, want 4 (leftover .old skipped)", len(recs))
	}
	for _, g := range store.idx {
		if g.tier == DiskLookupRaw {
			t.Fatal("leftover .old must not build a raw entry")
		}
	}
}

// TestIndexBootRawOnly covers the no-archive shape: .old raw plus
// active, with samples on the raw generation.
func TestIndexBootRawOnly(t *testing.T) {
	dir := t.TempDir()
	path := seedRawHistory(t, dir)
	store, err := NewHistoryStore(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadRecords(); err != nil {
		t.Fatal(err)
	}
	if len(store.idx) != 2 {
		t.Fatalf("idx gens = %d, want 2", len(store.idx))
	}
	raw := store.idx[0]
	if raw.tier != DiskLookupRaw || !raw.chained || raw.minH != 3 || raw.maxH != 4 {
		t.Fatalf("raw gen wrong: %+v", raw)
	}
	if len(raw.samples) == 0 {
		t.Fatal("raw gen must carry samples")
	}
}

func TestWindowBeforeLevels(t *testing.T) {
	dir := t.TempDir()
	path := seedRawHistory(t, dir)
	store, err := NewHistoryStore(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadRecords(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		level int
		want  []uint64
	}{
		{DiskLookupActive, []uint64{5, 6}},
		{DiskLookupRaw, []uint64{3, 4, 5, 6}},
	}
	for _, c := range cases {
		ring := newWindowRing(100)
		store.windowBefore(ring, 7, c.level)
		if got := heightsOf(t, ring.lines()); !equalHeights(got, c.want) {
			t.Fatalf("level %d = %v, want %v", c.level, got, c.want)
		}
	}
}

func TestWindowBeforeArchive(t *testing.T) {
	dir := t.TempDir()
	path := seedDiskHistory(t, dir)
	store, err := NewHistoryStore(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadRecords(); err != nil {
		t.Fatal(err)
	}
	ring := newWindowRing(100)
	store.windowBefore(ring, 7, DiskLookupArchive)
	if got := heightsOf(t, ring.lines()); !equalHeights(got, []uint64{1, 2, 5, 6}) {
		t.Fatalf("archive level = %v, want [1 2 5 6]", got)
	}
	// A cutoff inside the archive stops before the raw generations.
	ring = newWindowRing(100)
	store.windowBefore(ring, 2, DiskLookupArchive)
	if got := heightsOf(t, ring.lines()); !equalHeights(got, []uint64{1}) {
		t.Fatalf("before 2 = %v, want [1]", got)
	}
}

func equalHeights(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSendChatSegmentDisk wires the full path: RAM holds only a newer
// line, disk holds 1-2 archived plus 5-6 active, and the merged window
// must be exact at every lookup level.
func TestSendChatSegmentDisk(t *testing.T) {
	testCfg(t)
	dir := t.TempDir()
	path := seedDiskHistory(t, dir)
	store, err := NewHistoryStore(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadRecords(); err != nil {
		t.Fatal(err)
	}
	s := NewChatServer()
	s.Chain.Store = store
	// Simulate post-boot writes: RAM holds only a newer line the disk
	// never saw, so disk and RAM partition without overlap.
	s.Chain.appendMessageToHistory(tagLine(7, "chat", "", "seven"))

	drain := func(level int) HistorySync {
		cfg := config.DefaultDynamic()
		cfg.HistoryDiskLookup = level
		Cfg.Dynamic.Store(cfg)
		sess := &ClientSession{Send: make(chan []byte, 4096), DisplayName: "T#0000", Perms: GetDefaultPermission()}
		done := make(chan struct{})
		go func() {
			s.Chain.SendChatSegment(sess, 7, 10)
			close(done)
		}()
		timeout := time.After(10 * time.Second)
		var trailer HistorySync
		for {
			select {
			case msg := <-sess.Send:
				var hs HistorySync
				if err := json.Unmarshal(msg, &hs); err == nil && hs.Type == "history_sync" {
					trailer = hs
					<-done
					return trailer
				}
			case <-timeout:
				t.Fatal("segment stream stalled before trailer")
			}
		}
	}

	if tr := drain(DiskLookupOff); tr.Sent != 0 || tr.Total != 0 {
		t.Fatalf("level 0 must be exhausted, got %+v", tr)
	}
	if tr := drain(DiskLookupActive); tr.MinHeight != 5 || tr.MaxHeight != 6 {
		t.Fatalf("level 1 bounds = %+v, want 5-6", tr)
	}
	if tr := drain(DiskLookupRaw); tr.MinHeight != 5 || tr.MaxHeight != 6 {
		t.Fatalf("level 2 bounds = %+v, want 5-6 (no raw gen here)", tr)
	}
	if tr := drain(DiskLookupArchive); tr.MinHeight != 1 || tr.MaxHeight != 6 || tr.Sent != 4 || tr.Total != 4 {
		t.Fatalf("level 3 bounds = %+v, want 1,2,5,6 x4", tr)
	}
}

// TestRotateRelabelsIndex forces a rotation with a tiny size cap.
// Rename and compress run inside the same synchronous call, so only
// the end state is observable: the rotated content must surface as a
// bounds-only archive, and later writes must extend a fresh active
// generation (with samples) instead of the archived one.
func TestRotateRelabelsIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	store, err := NewHistoryStore(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.MaxSize = 400
	writeWire := func(height uint64) {
		t.Helper()
		var wire WireMessage
		if err := json.Unmarshal([]byte(tagLine(height, "chat", "", "line")), &wire); err != nil {
			t.Fatal(err)
		}
		if err := store.writeRecord(historyRecord{Wire: &wire}); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []uint64{1, 2, 3} {
		writeWire(h)
	}
	var archive, active *genIndex
	for i := range store.idx {
		switch store.idx[i].tier {
		case DiskLookupArchive:
			archive = &store.idx[i]
		case DiskLookupActive:
			active = &store.idx[i]
		case DiskLookupRaw:
			t.Fatalf("no steady-state raw gen expected, have %+v", store.idx[i])
		}
	}
	if archive == nil || !archive.chained || archive.minH != 1 || archive.maxH != 2 {
		t.Fatalf("compressed archive gen missing or wrong: %+v", store.idx)
	}
	if len(archive.samples) != 0 {
		t.Fatal("archive must keep bounds only")
	}
	if active == nil || !active.chained || active.minH != 3 || active.maxH != 3 {
		t.Fatalf("fresh active gen missing or wrong: %+v", store.idx)
	}
	if len(active.samples) == 0 {
		t.Fatal("active gen must carry samples for later writes")
	}
}

// TestSeekKeepsSampleLine pins the seek regression: with more than one
// stride of chained lines, a request past the second sample must still
// contain the sampled line itself (seeks land on line starts, so the
// first line after a seek is complete and must feed).
func TestSeekKeepsSampleLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	var lines []string
	for h := uint64(1); h <= 300; h++ {
		lines = append(lines, string(mustRecord(t, tagLine(h, "chat", "", "line"))))
	}
	writeTestLines(t, path, lines)
	store, err := NewHistoryStore(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadRecords(); err != nil {
		t.Fatal(err)
	}
	ring := newWindowRing(300)
	store.windowBefore(ring, 300, DiskLookupActive)
	got := heightsOf(t, ring.lines())
	if len(got) != 299 {
		t.Fatalf("seek scan = %d lines, want 299 (1-299)", len(got))
	}
	for i, h := range got {
		if h != uint64(i+1) {
			t.Fatalf("gap at position %d: got height %d", i, h)
		}
	}
}

// TestWindowBeforeDeepPaging pins the envelope bug: the cutoff search
// runs on raw disk lines (historyRecord envelopes), so it must unwrap
// rec.Wire.ChainHeight. With 40 heights, before=20 and limit=5, the
// window must be [15..19] — not an exhausted tail.
func TestWindowBeforeDeepPaging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	var lines []string
	for h := uint64(1); h <= 40; h++ {
		lines = append(lines, string(mustRecord(t, tagLine(h, "chat", "", "line"))))
	}
	writeTestLines(t, path, lines)
	store, err := NewHistoryStore(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadRecords(); err != nil {
		t.Fatal(err)
	}
	ring := newWindowRing(5)
	store.windowBefore(ring, 20, DiskLookupActive)
	if got := heightsOf(t, ring.lines()); !equalHeights(got, []uint64{15, 16, 17, 18, 19}) {
		t.Fatalf("deep page = %v, want [15 16 17 18 19]", got)
	}
}

// TestCollectBackwardSkipsGarbage pins counting on renderable lines: a
// tail of malformed lines must not eat the quota owed to older valid
// lines.
func TestCollectBackwardSkipsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	lines := []string{
		string(mustRecord(t, tagLine(1, "chat", "", "one"))),
		string(mustRecord(t, tagLine(2, "chat", "", "two"))),
		"{not json}",
		"{not json}",
		"{not json}",
	}
	writeTestLines(t, path, lines)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	got := collectBackward(f, fi.Size(), 2)
	if len(got) != 2 {
		t.Fatalf("garbage must not eat quota, got %d lines: %q", len(got), got)
	}
	if h := heightsOf(t, got); !equalHeights(h, []uint64{1, 2}) {
		t.Fatalf("got heights %v, want [1 2]", h)
	}
}

// TestFindCutoffZeroBound pins the tail contract: bound 0 never cuts,
// so callers that only reach disk with before != 0 stay the sole path.
func TestFindCutoffZeroBound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	writeTestLines(t, path, []string{string(mustRecord(t, tagLine(1, "chat", "", "one")))})
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, found, ok := findCutoff(f, nil, 0); !ok || found {
		t.Fatalf("bound 0 = (%v,%v), want (false,true)", found, ok)
	}
}
