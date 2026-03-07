package main

import (
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

func InitLogger(logFile string, maxSizeMB int) {
	if logFile == "" {
		return
	}

	rl := &RotatingLogger{
		Filename: logFile,
		MaxSize:  int64(maxSizeMB) * 1024 * 1024,
	}

	if err := rl.open(); err != nil {
		log.Fatalf("❌ Không thể mở file log: %v", err)
	}

	multiWriter := io.MultiWriter(os.Stdout, rl)
	log.SetOutput(multiWriter)
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
		l.rotate()
	}

	n, err = l.file.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *RotatingLogger) rotate() {
	if l.file != nil {
		_ = l.file.Close()
	}

	oldFile := l.Filename + ".old"
	_ = os.Rename(l.Filename, oldFile)

	l.size = 0
	_ = l.open()
}
