package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

type RotatingLogger struct {
	Filename string
	MaxSize  int64
	file     *os.File
	size     int64
	mu       sync.Mutex
}

func InitLogger(logFile string, maxSizeMB int) error {
	if logFile == "" {
		return nil
	}

	rl := &RotatingLogger{
		Filename: logFile,
		MaxSize:  int64(maxSizeMB) * 1024 * 1024,
	}

	if err := rl.open(); err != nil {
		return fmt.Errorf("không thể mở file log: %w", err)
	}

	multiWriter := io.MultiWriter(os.Stdout, rl)
	log.SetOutput(multiWriter)
	return nil
}

func (l *RotatingLogger) open() error {
	info, err := os.Stat(l.Filename)
	if err == nil {
		l.size = info.Size()
	}

	file, err := os.OpenFile(l.Filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.file = file
	return nil
}

func (l *RotatingLogger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	writeLen := int64(len(p))
	if l.size+writeLen > l.MaxSize {
		// Rotate best-effort: a failed rotate keeps l.file nil and the
		// nil guard below degrades to stdout-only.
		_ = l.rotate()
	}

	// open/rotate may fail (bad path, permissions): never nil-deref.
	// stdout still carries the line via MultiWriter.
	if l.file == nil {
		return len(p), nil
	}
	n, err = l.file.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *RotatingLogger) rotate() error {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}

	oldFile := l.Filename + ".old"
	_ = os.Rename(l.Filename, oldFile)

	l.size = 0
	return l.open()
}
