package main

import (
	"log"
	"os"
)

// Leveled logging over the shared std-log sink (stdout + rotating
// file, wired in InitLogger). Call sites pick a level; the only text the
// level adds is a grep-friendly [LEVEL] tag.
//
// logFatalf is what a boot failure uses: it reports the line at ERROR and
// then exits, so a process death reads like every other line instead of
// printing untagged. log.Fatal itself stays for the one case that has no
// format string of its own (the listener that returns an error).
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
