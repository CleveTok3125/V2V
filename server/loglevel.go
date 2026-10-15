package main

import (
	"log"
	"os"
)

// Leveled logging over the shared std-log sink (stdout + rotating file,
// wired in InitLogger). Call sites pick a level; the level only adds a
// grep-friendly [LEVEL] tag.
//
// logFatalf is what a boot failure uses: it logs at ERROR then exits, so
// a process death reads like every other line. log.Fatal itself stays for
// the one case with no format string of its own.
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

// logFatalf logs the failure at ERROR and then exits, the way log.Fatal
// exits. It exists so a boot failure carries the level and the tags the
// standard asks of every line.
func logFatalf(format string, args ...any) {
	logErrorf(format, args...)
	os.Exit(1)
}
