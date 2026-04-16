package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type envLoader struct {
	err error
}

func (l *envLoader) Smart(key string) string {
	if l.err != nil {
		return ""
	}

	val, err := getSmartEnv(key)
	if err != nil {
		l.err = err
		return ""
	}

	return val
}

func (l *envLoader) Int(key string) int {
	if l.err != nil {
		return 0
	}

	val, err := getEnvAsInt(key)
	if err != nil {
		l.err = err
		return 0
	}

	return val
}

func (l *envLoader) Duration(key string) time.Duration {
	if l.err != nil {
		return 0
	}

	val, err := getEnvAsDuration(key)
	if err != nil {
		l.err = err
		return 0
	}

	return val
}

func (l *envLoader) Err() error {
	return l.err
}

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

func getSmartEnv(key string) (string, error) {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return "", fmt.Errorf("thiếu biến môi trường bắt buộc: %s", key)
	}

	sysVal := os.Getenv(val)
	if sysVal != "" {
		return sysVal, nil
	}
	return val, nil
}

func getEnvAsInt(key string) (int, error) {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return 0, fmt.Errorf("thiếu biến môi trường bắt buộc: %s", key)
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("lỗi định dạng số ở biến %s: %w", key, err)
	}
	return parsed, nil
}

func getEnvAsIntOptional(key string, fallback int) int {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		log.Printf("⚠️ Lỗi định dạng số ở biến %s. Dùng mặc định: %d", key, fallback)
		return fallback
	}
	return parsed
}

func getEnvAsDuration(key string) (time.Duration, error) {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return 0, fmt.Errorf("thiếu biến môi trường bắt buộc: %s", key)
	}
	parsed, err := time.ParseDuration(val)
	if err != nil {
		return 0, fmt.Errorf("lỗi định dạng thời gian ở biến %s (ví dụ đúng: 200ms, 5s): %w", key, err)
	}
	return parsed, nil
}

func getEnvOptional(key string, fallback string) string {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return fallback
	}
	return val
}

func lastAfterDash(s string) string {
	if i := strings.LastIndex(s, "-"); i != -1 {
		return s[i+1:]
	}
	return s
}

func sanitizeString(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || unicode.IsGraphic(r) {
			if unicode.Is(unicode.Cf, r) {
				return -1
			}
			if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
				return -1
			}
			if unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
				return -1
			}
			return r
		}
		return -1
	}, text)
}

func getEnvAsBoolOptional(key string, fallback bool) bool {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		log.Printf("⚠️ Lỗi định dạng boolean ở biến %s. Dùng mặc định: %v", key, fallback)
		return fallback
	}
	return parsed
}

func generateRandomID(length int) string {
	bytesNeeded := (length + 1) / 2
	b := make([]byte, bytesNeeded)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}
