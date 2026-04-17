package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const historyQueueSize = 256

type HistoryStore struct {
	Filename string
	MaxSize  int64

	file *os.File
	size int64

	queue chan historyRecord
	mu    sync.Mutex
}

type historyRecord struct {
	Timestamp string `json:"ts"`
	Message   string `json:"msg"`
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
	for record := range h.queue {
		if err := h.writeRecord(record); err != nil {
			log.Printf("⚠️ [HISTORY] Không thể ghi history: %v", err)
		}
	}
}

func (h *HistoryStore) Enqueue(message string, now time.Time) {
	if h == nil {
		return
	}

	h.queue <- historyRecord{
		Timestamp: now.Format(time.RFC3339Nano),
		Message:   message,
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

	written, err := h.file.Write(line)
	h.size += int64(written)
	return err
}

func (h *HistoryStore) rotate() error {
	if h.file != nil {
		if err := h.file.Close(); err != nil {
			return err
		}
	}

	oldFile := h.Filename + ".old"
	if err := os.Rename(h.Filename, oldFile); err != nil && !os.IsNotExist(err) {
		return err
	}

	h.size = 0
	return h.open()
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

func (h *HistoryStore) loadFile(path string, messages *[]string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record historyRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			log.Printf("⚠️ [HISTORY] Bỏ qua record lỗi trong %s: %v", path, err)
			continue
		}
		if record.Message == "" {
			continue
		}
		*messages = append(*messages, record.Message)
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}
