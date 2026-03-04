package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func getEnvAsLocationOptional(key string, fallback string) *time.Location {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		val = fallback
	}
	loc, err := time.LoadLocation(val)
	if err != nil {
		log.Printf("⚠️ Cảnh báo: Múi giờ '%s' không hợp lệ. Đang dùng mặc định (Local).", val)
		return time.Local
	}
	return loc
}

func getSmartEnv(key string) string {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		log.Fatalf("❌ CRITICAL ERROR: Thiếu biến môi trường bắt buộc: %s", key)
	}

	sysVal := os.Getenv(val)
	if sysVal != "" {
		return sysVal
	}
	return val
}

func getEnvAsInt(key string) int {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		log.Fatalf("❌ CRITICAL ERROR: Thiếu biến môi trường bắt buộc: %s", key)
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		log.Fatalf("❌ Lỗi định dạng số ở biến %s: %v", key, err)
	}
	return parsed
}

func getEnvAsDuration(key string) time.Duration {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		log.Fatalf("❌ CRITICAL ERROR: Thiếu biến môi trường bắt buộc: %s", key)
	}
	parsed, err := time.ParseDuration(val)
	if err != nil {
		log.Fatalf("❌ Lỗi định dạng thời gian ở biến %s (ví dụ đúng: 200ms, 5s): %v", key, err)
	}
	return parsed
}

func lastAfterDash(s string) string {
	if i := strings.LastIndex(s, "-"); i != -1 {
		return s[i+1:]
	}
	return s
}

// sanitizeString depends on global AnsiRegex
func sanitizeString(text string) string {
	text = AnsiRegex.ReplaceAllString(text, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || unicode.IsGraphic(r) {
			if !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Me, r) {
				return r
			}
		}
		return -1
	}, text)
}
