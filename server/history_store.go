package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

const historyQueueSize = 256

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
}

type historyRecord struct {
	Timestamp string       `json:"ts"` // RFC3339Nano for readability
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
	if err := os.MkdirAll(filepath.Dir(h.Filename), 0o755); err != nil {
		return err
	}

	info, err := os.Stat(h.Filename)
	if err == nil {
		h.size = info.Size()
	} else if !os.IsNotExist(err) {
		return err
	}

	file, err := os.OpenFile(h.Filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
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
				log.Printf("⚠️ [HISTORY] Không thể ghi history: %v", err)
			}
		case <-ticker.C:
			h.mu.Lock()
			if h.file != nil && h.dirty {
				// Keep dirty on failure so the next tick retries.
				if err := h.file.Sync(); err != nil {
					log.Printf("⚠️ [HISTORY] Sync thất bại, thử lại tick sau: %v", err)
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
				log.Printf("⚠️ [HISTORY] Sync cuối thất bại: %v", err)
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

func (h *HistoryStore) loadZstdFile(path string) ([]historyRecord, error) {
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
	var out []historyRecord
	br := bufio.NewReader(r)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSuffix(line, []byte{'\n'})
			if len(line) > 0 {
				var rec historyRecord
				if err := json.Unmarshal(line, &rec); err != nil {
					log.Printf("⚠️ [HISTORY] Bỏ qua record lỗi trong %s: %v", path, err)
				} else if rec.Wire != nil || rec.Message != "" {
					out = append(out, rec)
				}
			}
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
		log.Printf("⚠️ [HISTORY] write queue full, dropping record (total drops=%d)", h.drops)
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
	h.size += int64(written)
	h.dirty = true
	return nil
}

func (h *HistoryStore) rotate() error {
	if h.file != nil {
		_ = h.file.Sync()
		if err := h.file.Close(); err != nil {
			return err
		}
	}

	oldFile := h.Filename + ".old"
	if err := os.Rename(h.Filename, oldFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Compress old file to .old.zst (best effort)
	if _, err := os.Stat(oldFile); err == nil {
		if err := compressFileZstd(oldFile, oldFile+".zst"); err != nil {
			log.Printf("⚠️ [HISTORY] Không thể nén history cũ: %v", err)
		} else if err := os.Remove(oldFile); err != nil {
			// A leftover .old next to a fresh .old.zst would double-load
			// every record on restart: fail loudly instead.
			return fmt.Errorf("cannot remove compressed-aside %s: %w", oldFile, err)
		} else {
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
		log.Printf("⚠️ [HISTORY] Reopen after rotate failed: %v", err)
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

func (h *HistoryStore) LoadMessages() ([]string, error) {
	if h == nil {
		return nil, nil
	}

	paths := []string{h.Filename + ".old", h.Filename}
	messages := make([]string, 0)

	for _, path := range paths {
		if err := h.loadFile(path, &messages); err != nil {
			return nil, err
		}
	}

	return messages, nil
}

func (h *HistoryStore) LoadRecords() ([]historyRecord, error) {
	if h == nil {
		return nil, nil
	}
	var records []historyRecord
	// Prefer .old.zst (new), fallback .old (legacy raw) for one version.
	// When both exist the .old is a leftover of the same generation:
	// skip it instead of double-loading every record.
	zstOK := false
	paths := []string{h.Filename + ".old.zst", h.Filename + ".old", h.Filename}
	for _, path := range paths {
		// Try zstd if suffix matches
		if strings.HasSuffix(path, ".zst") {
			if recs, err := h.loadZstdFile(path); err == nil {
				records = append(records, recs...)
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
			records = append(records, recs...)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return records, nil
}

func (h *HistoryStore) loadJSONLFile(path string) ([]historyRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []historyRecord
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSuffix(line, []byte{'\n'})
			if len(line) > 0 {
				var rec historyRecord
				if err := json.Unmarshal(line, &rec); err != nil {
					log.Printf("⚠️ [HISTORY] Bỏ qua record lỗi trong %s: %v", path, err)
				} else if rec.Message != "" || rec.Wire != nil {
					out = append(out, rec)
				}
			}
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

func (h *HistoryStore) loadFile(path string, messages *[]string) error {
	// Support both raw and zstd-compressed .old files
	tryPaths := []string{path}
	if strings.HasSuffix(path, ".old") {
		tryPaths = []string{path + ".zst", path}
	}
	for _, p := range tryPaths {
		file, err := os.Open(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		var reader *bufio.Reader
		var closeFn func()
		if strings.HasSuffix(p, ".zst") {
			// zstd decompress on the fly
			// Use helper to read zstd file line by line
			recs, err := h.loadZstdFile(p)
			file.Close()
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			for _, rec := range recs {
				if rec.Message != "" {
					*messages = append(*messages, rec.Message)
				} else if rec.Wire != nil {
					// Convert wire to legacy string for LoadMessages callers (history date/join)
					data, _ := json.Marshal(rec.Wire)
					*messages = append(*messages, string(data))
				}
			}
			return nil
		}
		reader = bufio.NewReader(file)
		closeFn = func() { file.Close() }
		// Read line-by-line with a Reader instead of a Scanner: a Scanner aborts
		// with "token too long" (and bricks startup) on any line >64KB, e.g. a
		// leftover oversized record. ReadBytes has no token cap, and the file is
		// bounded by rotation, so a single bad line can never prevent startup.
		for {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				line = bytes.TrimSuffix(line, []byte{'\n'})
				if len(line) > 0 {
					var record historyRecord
					if err := json.Unmarshal(line, &record); err != nil {
						log.Printf("⚠️ [HISTORY] Bỏ qua record lỗi trong %s: %v", p, err)
					} else if record.Message != "" {
						*messages = append(*messages, record.Message)
					} else if record.Wire != nil {
						data, _ := json.Marshal(record.Wire)
						*messages = append(*messages, string(data))
					}
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				closeFn()
				return readErr
			}
		}
		closeFn()
		return nil
	}
	return nil
}
