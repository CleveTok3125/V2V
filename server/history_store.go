package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

const historyQueueSize = 256

// Disk lookup tiers for on-demand older segments beyond RAM, matching
// HISTORY_DISK_LOOKUP levels: each tier adds one older generation.
// Active is cheapest (hot indexed file), the archive costs a full
// streaming decode per request.
const (
	DiskLookupOff     = iota // RAM only
	DiskLookupActive         // +history.jsonl
	DiskLookupRaw            // +.old raw
	DiskLookupArchive        // +.old.zst (full)
)

type HistoryStore struct {
	Filename string
	MaxSize  int64

	file  *os.File
	size  int64
	dirty bool

	queue chan historyRecord
	mu    sync.Mutex
	// closed guards EnqueueWire against send-on-closed-queue panics;
	// drops counts records shed under backpressure.
	closed bool
	drops  uint64

	// idx orders disk generations old→new for on-demand paging. The
	// active generation sits last while it exists. Guarded by mu for
	// writes; scanners snapshot it and release before file I/O.
	idx []genIndex
}

// genIndex describes one disk generation for paging older segments.
// Samples map chained heights to raw-file byte offsets (sparse, every
// indexSampleStride lines); the archive keeps bounds only because its
// compressed offsets are not seekable.
type genIndex struct {
	tier    int
	minH    uint64
	maxH    uint64
	chained bool
	samples []heightSample
	// sinceSample counts chained lines since the last sample; only the
	// active generation extends at runtime.
	sinceSample int
}

type heightSample struct {
	height uint64
	offset int64
}

// indexSampleStride bounds the forward scan after a seek: at most this
// many chained lines (plus whatever unchained lines interleave) are
// read before the cutoff.
const indexSampleStride = 256

// indexedRecord pairs a parsed record with its line byte offset in a
// raw file. Archive (zstd) offsets count decompressed bytes and are
// recorded for uniformity but never sampled: only raw offsets seek.
type indexedRecord struct {
	rec    historyRecord
	offset int64
}

type historyRecord struct {
	Timestamp string       `json:"ts"`            // RFC3339Nano for readability
	Message   string       `json:"msg,omitempty"` // system messages (date/join/leave) remain as plain string
	Wire      *WireMessage `json:"wire,omitempty"`
}

func NewHistoryStore(path string, maxSizeMB int) (*HistoryStore, error) {
	if path == "" {
		return nil, nil
	}

	store := &HistoryStore{
		Filename: path,
		MaxSize:  int64(maxSizeMB) * 1024 * 1024,
		queue:    make(chan historyRecord, historyQueueSize),
	}

	if err := store.open(); err != nil {
		return nil, fmt.Errorf("không thể mở file history: %w", err)
	}

	go store.writeLoop()
	return store, nil
}

func (h *HistoryStore) open() error {
	if err := os.MkdirAll(filepath.Dir(h.Filename), 0o700); err != nil {
		return err
	}

	info, err := os.Stat(h.Filename)
	if err == nil {
		h.size = info.Size()
	} else if !os.IsNotExist(err) {
		return err
	}

	// Chat plaintext stays owner-only. Existing files keep whatever
	// mode they already have (OpenFile never rechmods): deployments
	// predating this must chmod once, see the commit message.
	file, err := os.OpenFile(h.Filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	h.file = file
	return nil
}

func (h *HistoryStore) writeLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case record, ok := <-h.queue:
			if !ok {
				return
			}
			if err := h.writeRecord(record); err != nil {
				logWarnf("⚠️ [HISTORY] Không thể ghi history: %v", err)
			}
		case <-ticker.C:
			h.mu.Lock()
			if h.file != nil && h.dirty {
				// Keep dirty on failure so the next tick retries.
				if err := h.file.Sync(); err != nil {
					logWarnf("⚠️ [HISTORY] Sync thất bại, thử lại tick sau: %v", err)
				} else {
					h.dirty = false
				}
			}
			h.mu.Unlock()
		}
	}
}

func (h *HistoryStore) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	close(h.queue)
	if h.file != nil {
		if h.dirty {
			if err := h.file.Sync(); err != nil {
				logWarnf("⚠️ [HISTORY] Sync cuối thất bại: %v", err)
			} else {
				h.dirty = false
			}
		}
		err := h.file.Close()
		h.mu.Unlock()
		return err
	}
	h.mu.Unlock()
	return nil
}

func (h *HistoryStore) loadZstdFile(path string) ([]indexedRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := zstd.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []indexedRecord
	var offset int64
	br := bufio.NewReader(r)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			raw := bytes.TrimSuffix(line, []byte{'\n'})
			if len(raw) > 0 {
				var rec historyRecord
				if err := json.Unmarshal(raw, &rec); err != nil {
					logWarnf("⚠️ [HISTORY] Bỏ qua record lỗi trong %s: %v", path, err)
				} else if rec.Wire != nil || rec.Message != "" {
					out = append(out, indexedRecord{rec: rec, offset: offset})
				}
			}
			offset += int64(len(line))
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, readErr
		}
	}
	return out, nil
}

func (h *HistoryStore) EnqueueWire(wire WireMessage, now time.Time) {
	if h == nil {
		return
	}
	// Never block the broadcast path on a stalled disk, and never panic
	// on send-after-Close: shed load with a counter instead.
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	select {
	case h.queue <- historyRecord{
		Timestamp: now.Format(time.RFC3339Nano),
		Wire:      &wire,
	}:
	default:
		h.drops++
		logWarnf("⚠️ [HISTORY] write queue full, dropping record (total drops=%d)", h.drops)
	}
}

func (h *HistoryStore) writeRecord(record historyRecord) error {
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()

	// A prior rotate may have failed after closing the handle (rename or
	// unlink error), leaving the store fileless. Reopen here so history
	// writing self-heals instead of stalling until the next restart.
	if h.file == nil {
		if err := h.open(); err != nil {
			h.drops++
			return fmt.Errorf("history store reopen failed: %w", err)
		}
	}
	if h.MaxSize > 0 && h.size+int64(len(line)) > h.MaxSize {
		if err := h.rotate(); err != nil {
			return err
		}
	}
	if h.file == nil {
		h.drops++
		return errors.New("history store has no open file")
	}

	written, err := h.file.Write(line)
	if err != nil {
		return err
	}
	offset := h.size
	h.size += int64(written)
	h.dirty = true
	if record.Wire != nil && record.Wire.ChainHeight != 0 {
		h.indexWroteLocked(record.Wire.ChainHeight, offset)
	}
	return nil
}

// indexWroteLocked extends the active generation after a successful
// append: h.size before the write is the new line's byte offset.
// Caller must hold mu (writeRecord does).
func (h *HistoryStore) indexWroteLocked(height uint64, offset int64) {
	var g *genIndex
	if n := len(h.idx); n > 0 && h.idx[n-1].tier == DiskLookupActive {
		g = &h.idx[n-1]
	} else {
		h.idx = append(h.idx, genIndex{tier: DiskLookupActive})
		g = &h.idx[len(h.idx)-1]
	}
	if !g.chained {
		g.chained = true
		g.minH, g.maxH = height, height
	} else {
		if height < g.minH {
			g.minH = height
		}
		if height > g.maxH {
			g.maxH = height
		}
	}
	if g.sinceSample%indexSampleStride == 0 {
		g.samples = append(g.samples, heightSample{height: height, offset: offset})
	}
	g.sinceSample++
}

func (h *HistoryStore) rotate() error {
	if h.file != nil {
		_ = h.file.Sync()
		closeErr := h.file.Close()
		// Drop the closed handle and its size before any early return:
		// a rotate failure must leave a consistent fileless store that
		// writeRecord can reopen, not a stale handle into a closed file.
		h.file = nil
		h.size = 0
		if closeErr != nil {
			return closeErr
		}
	}

	oldFile := h.Filename + ".old"
	renameErr := os.Rename(h.Filename, oldFile)
	if renameErr != nil && !os.IsNotExist(renameErr) {
		return renameErr
	}
	if renameErr == nil {
		// The rename clobbered any previous .old content and moved the
		// active generation there intact: byte offsets stay valid, only
		// the tier label changes. Caller holds mu (writeRecord does).
		h.dropGenLocked(DiskLookupRaw)
		if !h.relabelGenLocked(DiskLookupActive, DiskLookupRaw) {
			// Active file existed but held no chained line (or no
			// entry): still record the generation so its unchained
			// lines stay reachable. Appending last keeps old→new
			// order: the next write adds the new active after it.
			h.idx = append(h.idx, genIndex{tier: DiskLookupRaw})
		}
	}
	// Compress old file to .old.zst (best effort)
	if _, err := os.Stat(oldFile); err == nil {
		if err := compressFileZstd(oldFile, oldFile+".zst"); err != nil {
			logWarnf("⚠️ [HISTORY] Không thể nén history cũ: %v", err)
		} else if err := os.Remove(oldFile); err != nil {
			// A leftover .old next to a fresh .old.zst would double-load
			// every record on restart: fail loudly instead. Mirror the
			// loader's skip (.old ignored when .zst exists): drop the
			// old archive, then relabel the raw entry to the fresh
			// archive bounds so paging still serves the new content.
			// (If no raw entry exists — an unchained-only .old builds
			// none — the archive stays unindexed until the next boot
			// load; files stay correct, only deep paging misses it.)
			h.dropGenLocked(DiskLookupArchive)
			h.relabelGenLocked(DiskLookupRaw, DiskLookupArchive)
			return fmt.Errorf("cannot remove compressed-aside %s: %w", oldFile, err)
		} else {
			// Archive clobbered the previous .zst; compressed offsets
			// cannot seek, so only bounds survive.
			h.dropGenLocked(DiskLookupArchive)
			h.relabelGenLocked(DiskLookupRaw, DiskLookupArchive)
			// fsync dir for durability (like webauthn_store)
			if dir, err := os.Open(filepath.Dir(h.Filename)); err == nil {
				_ = dir.Sync()
				_ = dir.Close()
			}
		}
	} else {
		if dir, err := os.Open(filepath.Dir(h.Filename)); err == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}

	h.size = 0
	h.file = nil
	if err := h.open(); err != nil {
		// Stay fileless: writeRecord drops with a counter instead of
		// writing into the closed pre-rotate handle. A later record
		// retries the open via the same path.
		logWarnf("⚠️ [HISTORY] Reopen after rotate failed: %v", err)
		return err
	}
	return nil
}

func compressFileZstd(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// Write to a temp file and rename: a mid-copy failure must never
	// leave a truncated dst behind for LoadRecords to choke on.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*.zst")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	failed := true
	defer func() {
		if failed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	enc, err := zstd.NewWriter(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(enc, in); err != nil {
		_ = enc.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	failed = false
	return os.Rename(tmpName, dst)
}

func (h *HistoryStore) LoadRecords() ([]historyRecord, error) {
	if h == nil {
		return nil, nil
	}
	// Rebuild the page index from scratch: LoadRecords must stay
	// idempotent no matter how often boot or tests call it.
	h.mu.Lock()
	h.idx = nil
	h.mu.Unlock()
	var records []historyRecord
	// Prefer .old.zst (new), fallback .old (legacy raw) for one version.
	// When both exist the .old is a leftover of the same generation:
	// skip it instead of double-loading every record. The index mirrors
	// exactly which files contribute records.
	zstOK := false
	paths := []string{h.Filename + ".old.zst", h.Filename + ".old", h.Filename}
	for _, path := range paths {
		// Try zstd if suffix matches
		if strings.HasSuffix(path, ".zst") {
			if recs, err := h.loadZstdFile(path); err == nil {
				for _, ir := range recs {
					records = append(records, ir.rec)
				}
				h.indexLoaded(DiskLookupArchive, recs)
				zstOK = true
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			continue
		}
		if path == h.Filename+".old" && zstOK {
			continue
		}
		if recs, err := h.loadJSONLFile(path); err == nil {
			for _, ir := range recs {
				records = append(records, ir.rec)
			}
			tier := DiskLookupActive
			if path != h.Filename {
				tier = DiskLookupRaw
			}
			h.indexLoaded(tier, recs)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return records, nil
}

// indexLoaded records one loaded generation for paging: bounds
// always, byte-offset samples for raw files only (archive offsets are
// decompressed-stream positions and cannot seek). Empty files build no
// entry, so scans never open them per request. Caller holds no lock;
// the store takes mu for the append. LoadRecords runs before any
// request can arrive; a racing writeRecord serializes on the same mu.
func (h *HistoryStore) indexLoaded(tier int, recs []indexedRecord) {
	if len(recs) == 0 {
		return
	}
	var g genIndex
	g.tier = tier
	for _, ir := range recs {
		var height uint64
		if ir.rec.Wire != nil {
			height = ir.rec.Wire.ChainHeight
		}
		if height == 0 {
			continue
		}
		if !g.chained {
			g.chained = true
			g.minH, g.maxH = height, height
		} else {
			if height < g.minH {
				g.minH = height
			}
			if height > g.maxH {
				g.maxH = height
			}
		}
		if tier != DiskLookupArchive {
			if g.sinceSample%indexSampleStride == 0 {
				g.samples = append(g.samples, heightSample{height: height, offset: ir.offset})
			}
			g.sinceSample++
		}
	}
	h.mu.Lock()
	h.idx = append(h.idx, g)
	h.mu.Unlock()
}

func (h *HistoryStore) loadJSONLFile(path string) ([]indexedRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []indexedRecord
	var offset int64
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			raw := bytes.TrimSuffix(line, []byte{'\n'})
			if len(raw) > 0 {
				var rec historyRecord
				if err := json.Unmarshal(raw, &rec); err != nil {
					logWarnf("⚠️ [HISTORY] Bỏ qua record lỗi trong %s: %v", path, err)
				} else if rec.Message != "" || rec.Wire != nil {
					out = append(out, indexedRecord{rec: rec, offset: offset})
				}
			}
			offset += int64(len(line))
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, readErr
		}
	}
	return out, nil
}

// genPath resolves the current file for one tier. Rotation renames
// files underfoot, so scanners resolve names at request time and
// tolerate absence (stale index after external deletion).
func (h *HistoryStore) genPath(tier int) string {
	switch tier {
	case DiskLookupArchive:
		return h.Filename + ".old.zst"
	case DiskLookupRaw:
		return h.Filename + ".old"
	default:
		return h.Filename
	}
}

// dropGenLocked removes every index entry of one tier. Caller holds mu.
func (h *HistoryStore) dropGenLocked(tier int) {
	kept := h.idx[:0]
	for _, g := range h.idx {
		if g.tier != tier {
			kept = append(kept, g)
		}
	}
	for i := len(kept); i < len(h.idx); i++ {
		h.idx[i] = genIndex{}
	}
	h.idx = kept
}

// relabelGenLocked moves the first entry of one tier to another (same
// content, new role after rotation) and reports whether one existed.
// Samples survive Active→Raw (offsets intact); Raw→Archive drops them
// (compressed offsets cannot seek) but keeps bounds. Caller holds mu.
func (h *HistoryStore) relabelGenLocked(from, to int) bool {
	for i := range h.idx {
		if h.idx[i].tier == from {
			h.idx[i].tier = to
			if to == DiskLookupArchive {
				h.idx[i].samples = nil
			}
			return true
		}
	}
	return false
}

// windowBefore merges disk generations old→new into ring, stopping at
// the first chained height at or above bound (excluded). Generations
// above level are skipped; the caller bounds disk against RAM first so
// chained content never overlaps what RAM will feed. Unchained lines
// travel by position, which may repeat a few across the RAM seam.
// Files are opened per request and never locked: rotation renames
// atomically, so a scan sees a consistent (if slightly stale) view.
// Missing files (stale index, concurrent rotation) are skipped, never
// fatal. Transient memory stays within generations×limit lines.
func (h *HistoryStore) windowBefore(ring *windowRing, bound uint64, level int) {
	if h == nil || ring == nil {
		return
	}
	h.mu.Lock()
	gens := append([]genIndex(nil), h.idx...)
	h.mu.Unlock()
	var temps [][]string
	for _, g := range gens {
		if g.tier > level || g.tier <= DiskLookupOff {
			continue
		}
		t, done := h.scanGenTemp(bound, g, ring.limit)
		temps = append(temps, t)
		if done {
			break
		}
	}
	// Temps hold converted lines (recordLine applied once at scan, so
	// malformed lines never occupy quota); the feed re-checks the
	// cutoff defensively but never hits it: every temp precedes it by
	// construction.
	for _, t := range temps {
		for _, msgStr := range t {
			if ring.feed(msgStr, bound) {
				return
			}
		}
	}
}

// seekOffset returns the byte offset to start scanning a raw
// generation: the nearest sample strictly below bound, or 0. Samples
// ascend with heights.
func seekOffset(samples []heightSample, bound uint64) int64 {
	i := sort.Search(len(samples), func(i int) bool { return samples[i].height >= bound })
	if i == 0 {
		return 0
	}
	return samples[i-1].offset
}

// scanGenTemp returns up to limit pre-cutoff lines (oldest→newest) from
// one generation plus whether the cutoff was reached (newer generations
// excluded). Raw files seek to the nearest sample, find the cutoff
// forward, then collect backward; the archive (not seekable) streams
// forward with a capped ring. Missing files scan as empty, never fatal.
// A torn tail (concurrent append without its newline yet) is ignored,
// never fed.
func (h *HistoryStore) scanGenTemp(bound uint64, g genIndex, limit int) ([]string, bool) {
	path := h.genPath(g.tier)
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if g.tier == DiskLookupArchive {
		r, err := zstd.NewReader(f)
		if err != nil {
			logWarnf("⚠️ [HISTORY] archive %s unreadable, skipping: %v", path, err)
			return nil, false
		}
		defer r.Close()
		ring := newWindowRing(limit)
		if streamForward(ring, bound, bufio.NewReader(r)) {
			return ring.lines(), true
		}
		return ring.lines(), false
	}
	off, found, ok := findCutoff(f, g.samples, bound)
	if !ok {
		// Seek failed (stale index, replaced file): exact but slower
		// full forward stream instead of guessing alignment.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, false
		}
		ring := newWindowRing(limit)
		if streamForward(ring, bound, bufio.NewReader(f)) {
			return ring.lines(), true
		}
		return ring.lines(), false
	}
	var end int64
	if found {
		end = off
	} else {
		fi, err := f.Stat()
		if err != nil {
			return nil, false
		}
		end = fi.Size()
	}
	// collectBackward converts (malformed drops); temps feed directly.
	return collectBackward(f, end, limit), found
}

// findCutoff returns the line-start offset of the first chained height
// at or above bound in a raw file. ok=false means seeking failed (the
// caller falls back to a full stream); found=false means the whole file
// precedes the cutoff. Sample offsets are line starts by construction,
// verified with a one-byte probe so a replaced file rescans from zero
// instead of dropping a valid line.
func findCutoff(f *os.File, samples []heightSample, bound uint64) (off int64, found, ok bool) {
	// bound 0 means the tail window (no cutoff); callers only reach
	// disk with before != 0, but never let a direct call turn the
	// first chained line into a bogus cutoff.
	if bound == 0 {
		return 0, false, true
	}
	start := seekOffset(samples, bound)
	if start > 0 {
		var b [1]byte
		if _, err := f.ReadAt(b[:], start-1); err != nil || b[0] != '\n' {
			return 0, false, false
		}
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return 0, false, false
		}
	}
	br := bufio.NewReader(f)
	var cur int64 = start
	for {
		raw, err := br.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			line := bytes.TrimSuffix(raw, []byte{'\n'})
			// Disk lines are historyRecord envelopes: the height
			// lives in rec.Wire, never top-level. Unwrap like the
			// loader instead of reading the envelope itself.
			if len(line) > 0 {
				if rec := parseRecord(line); rec.Wire != nil && rec.Wire.ChainHeight != 0 && rec.Wire.ChainHeight >= bound {
					return cur, true, true
				}
			}
			cur += int64(len(raw))
		} else if len(raw) > 0 {
			// Torn tail: no complete line follows in this pass.
			return 0, false, true
		}
		if err != nil {
			return 0, false, true
		}
	}
}

// streamForward feeds complete lines oldest→newest until the cutoff,
// reporting whether it was reached. Malformed lines are skipped like
// the loader skips them; only whole newline-terminated lines feed.
func streamForward(ring *windowRing, bound uint64, br *bufio.Reader) bool {
	for {
		raw, err := br.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			line := bytes.TrimSuffix(raw, []byte{'\n'})
			if len(line) > 0 {
				if msgStr, ok := recordLine(parseRecord(line)); ok {
					if ring.feed(msgStr, bound) {
						return true
					}
				}
			}
		}
		if err != nil {
			return false
		}
	}
}

const backwardChunk = 32 * 1024
const maxTornProbe = 64 * 1024

// collectBackward returns up to maxLines converted lines ending at
// endOff (exclusive), oldest→newest. endOff is a cutoff line start or
// a file end (torn tail adjusted away). Conversion (and its malformed
// filter) happens here so maxLines counts renderable lines, exactly
// like the forward path counts fed lines.
func collectBackward(f *os.File, endOff int64, maxLines int) []string {
	if maxLines <= 0 || endOff <= 0 {
		return nil
	}
	// Clamp a stale end offset (file replaced smaller mid-scan) to the
	// live EOF: backward collection degrades to the tail, never errors.
	if fi, err := f.Stat(); err == nil && endOff > fi.Size() {
		endOff = fi.Size()
	}
	endOff = completeEnd(f, endOff)
	keep := func(raw []byte) []byte {
		if len(raw) == 0 {
			return nil
		}
		if msgStr, ok := recordLine(parseRecord(raw)); ok {
			return []byte(msgStr)
		}
		return nil
	}
	var rev [][]byte // converted lines, newest→oldest
	var carry []byte // oldest fragment, completed by the next chunk
	pos := endOff
outer:
	for pos > 0 && len(rev) < maxLines {
		n := int64(backwardChunk)
		if n > pos {
			n = pos
		}
		off := pos - n
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, off); err != nil {
			break
		}
		// data ends at the previous pos: a line boundary on the
		// first iteration (adjusted end), a carried seam after that.
		// Either way only parts[0] may be partial (unless BOF).
		data := append(buf, carry...)
		parts := bytes.Split(data, []byte{'\n'})
		for i := len(parts) - 1; i >= 1; i-- {
			if msg := keep(parts[i]); msg != nil {
				rev = append(rev, msg)
				if len(rev) >= maxLines {
					break outer
				}
			}
		}
		carry = parts[0]
		pos = off
	}
	// File head is always a line start (append-only from empty), so the
	// leftover head fragment is a complete first line.
	if pos == 0 && len(carry) > 0 && len(rev) < maxLines {
		if msg := keep(carry); msg != nil {
			rev = append(rev, msg)
		}
	}
	out := make([]string, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, string(rev[i]))
	}
	return out
}

// completeEnd moves a file-end offset back past a torn tail (a trailing
// fragment without its newline is a concurrent append in flight).
// Cutoff offsets are line starts already, so the probe is a no-op for
// them. Lines are small (chat cap ~KBs); the probe window only needs to
// cover one maximal line.
func completeEnd(f *os.File, end int64) int64 {
	if end <= 0 {
		return 0
	}
	n := int64(maxTornProbe)
	if n > end {
		n = end
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, end-n); err != nil {
		return end
	}
	if buf[len(buf)-1] == '\n' {
		return end
	}
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		return end - n + int64(i) + 1
	}
	return end - n
}

// parseRecord decodes one history line, mirroring the loader's
// leniency (malformed JSON skipped, empties dropped).
func parseRecord(line []byte) historyRecord {
	var rec historyRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return historyRecord{}
	}
	return rec
}
