package serverconfig

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// warnings accumulates non-fatal configuration problems encountered while
// loading. The caller logs the returned lines; this package never logs.
type warnings struct {
	list []string
}

func (w *warnings) addf(format string, args ...any) {
	w.list = append(w.list, fmt.Sprintf(format, args...))
}

// EnvLoader reads required values with a shared error latch: the first
// failure is kept and Err reports it, so a partially built config is
// never usable.
type EnvLoader struct {
	err error
}

func (l *EnvLoader) Smart(key string) string {
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

func (l *EnvLoader) Int(key string) int {
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

func (l *EnvLoader) Duration(key string) time.Duration {
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

func (l *EnvLoader) Err() error {
	return l.err
}

// Optional reads a string with Smart semantics (indirection through a
// named variable) but a missing/empty value yields "" instead of an
// error. Distinct from the *Fallback family, which never indirects.
func (l *EnvLoader) Optional(key string) string {
	if l.err != nil {
		return ""
	}

	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return ""
	}
	// Smart indirection: a value naming another variable resolves to
	// that variable's value (e.g. STATUS_URL=SHARED_URL).
	if sysVal := os.Getenv(val); sysVal != "" {
		return sysVal
	}
	return val
}

// GetEnvAsInt parses a required integer with no warning channel.
func GetEnvAsInt(key string) (int, error) { return getEnvAsInt(key) }

// GetEnvAsDuration parses a required duration with no warning channel.
func GetEnvAsDuration(key string) (time.Duration, error) { return getEnvAsDuration(key) }

// GetEnvFallback reads a string, falling back when unset or empty.
func GetEnvFallback(key, fallback string) string { return getEnvFallback(key, fallback) }

// GetEnvAsBoolFallback parses a boolean, falling back on a missing or
// malformed value. Format warnings are discarded; loaders that surface
// them use the collector form instead.
func GetEnvAsBoolFallback(key string, fallback bool) bool {
	var w warnings
	return getEnvAsBoolFallback(&w, key, fallback)
}

func getEnvAsLocationFallback(w *warnings, key string, fallback string) *time.Location {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		val = fallback
	}
	loc, err := time.LoadLocation(val)
	if err != nil {
		w.addf("⚠️ Cảnh báo: Múi giờ '%s' không hợp lệ. Đang dùng mặc định (Local).", val)
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

func getEnvAsIntFallback(w *warnings, key string, fallback int) int {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		w.addf("⚠️ Lỗi định dạng số ở biến %s. Dùng mặc định: %d", key, fallback)
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

func getEnvFallback(key string, fallback string) string {
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

func getEnvAsBoolFallback(w *warnings, key string, fallback bool) bool {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		w.addf("⚠️ Lỗi định dạng boolean ở biến %s. Dùng mặc định: %v", key, fallback)
		return fallback
	}
	return parsed
}

func generateRandomID(length int) string {
	bytesNeeded := (length + 1) / 2
	b := make([]byte, bytesNeeded)
	// Entropy exhaustion must not yield a zero ID: fall back to time-based.
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)[:length]
}
