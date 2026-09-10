package main

import "log"

// Leveled logging over the shared std-log sink (stdout + rotating
// file, wired in InitLogger). Call sites pick a level; message bodies
// stay byte-identical to the unlevelled text, only a grep-friendly
// [LEVEL] tag is prepended. log.Fatal stays for process death, which
// is not a level.
func logInfof(format string, args ...any) {
	log.Printf("[INFO] "+format, args...)
}

// Info logs a Println-style informational line.
func logInfo(args ...any) {
	args = append([]any{"[INFO]"}, args...)
	log.Println(args...)
}

func logWarnf(format string, args ...any) {
	log.Printf("[WARN] "+format, args...)
}

func logErrorf(format string, args ...any) {
	log.Printf("[ERROR] "+format, args...)
}
