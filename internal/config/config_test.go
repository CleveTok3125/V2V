package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/identity"
)

func TestMetaShowDefault(t *testing.T) {
	c := DefaultClientConfig()
	if !c.ShowMeta() {
		t.Fatal("default must show meta lines")
	}
	var nilCfg *ClientConfig
	if !nilCfg.ShowMeta() {
		t.Fatal("nil config must fall back to shown")
	}
}

func TestMetaShowBackfill(t *testing.T) {
	dir := t.TempDir()
	// Old config without the meta section backfills to shown.
	old := map[string]any{"defaults": map[string]any{"username": "A"}}
	data, _ := json.Marshal(old)
	path := filepath.Join(dir, "config.jsonc")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.ShowMeta() {
		t.Fatal("absent meta section must backfill to shown")
	}
	// Explicit false survives the round trip.
	raw, _ := json.Marshal(map[string]any{"ui": map[string]any{"meta": map[string]any{"show": false}}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ShowMeta() {
		t.Fatal("explicit false must be honored")
	}
}

func TestLimitsBackfill(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"ui": map[string]any{"meta": map[string]any{"show": false}}})
	path := filepath.Join(dir, "config.jsonc")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultClientConfig()
	if c.Limits.MaxMessageLength != def.Limits.MaxMessageLength {
		t.Fatalf("MaxMessageLength = %d, want default %d", c.Limits.MaxMessageLength, def.Limits.MaxMessageLength)
	}
	if c.Limits.MessageCooldown != def.Limits.MessageCooldown {
		t.Fatal("MessageCooldown must backfill")
	}
	if c.ShowMeta() {
		t.Fatal("explicit meta false must survive alongside backfill")
	}
}

func TestMentionReplyDefaults(t *testing.T) {
	c := DefaultClientConfig()
	if !c.MentionEnabled() || !c.ReplyEnabled() {
		t.Fatal("defaults must enable mention and reply")
	}
	if c.MentionColor() != [3]int{0, 255, 255} {
		t.Fatalf("default color = %v", c.MentionColor())
	}
	if c.QuoteMaxRunes() != 80 {
		t.Fatalf("default runes = %d", c.QuoteMaxRunes())
	}
}

func TestMentionReplyBackfillAndClamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	raw, _ := json.Marshal(map[string]any{"ui": map[string]any{
		"mention": map[string]any{"enabled": false, "color": []int{300, -5, 128}},
		"reply":   map[string]any{"quoteMaxRunes": 5000},
	}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.MentionEnabled() {
		t.Fatal("explicit mention false must stick")
	}
	if c.MentionColor() != [3]int{255, 0, 128} {
		t.Fatalf("color must clamp, got %v", c.MentionColor())
	}
	if !c.ReplyEnabled() {
		t.Fatal("absent reply.enabled must backfill true")
	}
	if c.QuoteMaxRunes() != 200 {
		t.Fatalf("runes must clamp to 200, got %d", c.QuoteMaxRunes())
	}
}

func TestClipboardClearAfterSec(t *testing.T) {
	c := DefaultClientConfig()
	if c.ClipboardClearAfterSec() != 30 {
		t.Fatalf("default = %d, want 30", c.ClipboardClearAfterSec())
	}
	var nilCfg *ClientConfig
	if nilCfg.ClipboardClearAfterSec() != 30 {
		t.Fatal("nil config must fall back to 30")
	}
	zero := 0
	c.UI.Clipboard.ClearAfterSec = &zero
	if c.ClipboardClearAfterSec() != 0 {
		t.Fatal("explicit 0 must disable")
	}
	neg := -5
	c.UI.Clipboard.ClearAfterSec = &neg
	if c.ClipboardClearAfterSec() != 30 {
		t.Fatalf("negative must fall back to 30, got %d", c.ClipboardClearAfterSec())
	}
	v := 90
	c.UI.Clipboard.ClearAfterSec = &v
	if c.ClipboardClearAfterSec() != 90 {
		t.Fatalf("got %d, want 90", c.ClipboardClearAfterSec())
	}
}

func TestLoadMissingUsesDefaultsWithoutCreating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultClientConfig()
	if c.Limits.MaxMessageLength != def.Limits.MaxMessageLength {
		t.Fatal("missing file must yield defaults")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Load must never create the config file")
	}
}

func TestLoadEncryptedRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	raw := `{"defaults": {"username": "Sealed"}} // sealed config`
	sealed, err := identity.EncryptData([]byte(raw), []byte("correct-horse"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("plain Load of sealed file must return ErrEncrypted, got %v", err)
	}
	c, err := LoadEncrypted(path, []byte("correct-horse"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Defaults.Username != "Sealed" {
		t.Fatalf("decrypted username = %q, want Sealed", c.Defaults.Username)
	}
	if _, err := LoadEncrypted(path, []byte("wrong")); err == nil {
		t.Fatal("wrong passphrase must fail closed")
	}
}

func TestReplaceFileSwapsAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	if err := ReplaceFile(path, []byte(`{"a": 1}`)); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFile(path, []byte(`{"a": 2}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"a": 2}` {
		t.Fatalf("replace must swap whole content, got %q, %v", data, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("replace must keep owner-only perms: %v", err)
	}
}

func TestClipboardBackfill(t *testing.T) {
	dir := t.TempDir()
	old := map[string]any{"defaults": map[string]any{"username": "A"}}
	data, _ := json.Marshal(old)
	path := filepath.Join(dir, "config.jsonc")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.Clipboard.ClearAfterSec == nil || c.ClipboardClearAfterSec() != 30 {
		t.Fatal("absent clipboard section must backfill to 30")
	}
	raw, _ := json.Marshal(map[string]any{"ui": map[string]any{"clipboard": map[string]any{"clearAfterSec": 0}}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ClipboardClearAfterSec() != 0 {
		t.Fatal("explicit 0 must survive backfill (disable)")
	}
}

// History paging knobs pin their defaults: 2s segment throttle,
// disk lookup off (opt-in per deployment cost).
func TestHistoryRecoveryDefaults(t *testing.T) {
	d := DefaultClientConfig()
	if got := d.HistoryInitialLines(); got != 500 {
		t.Fatalf("default initialLines = %d, want 500", got)
	}
	if got := d.HistoryBatchLines(); got != 100 {
		t.Fatalf("default batchLines = %d, want 100", got)
	}
	if got := d.HistoryRecoverRetries(); got != 2 {
		t.Fatalf("default recoverRetries = %d, want 2", got)
	}
	if got := d.HistoryRecoverRetryDelay(); got != 2*time.Second {
		t.Fatalf("default recoverRetryDelay = %v, want 2s", got)
	}
	if got := d.HistoryLiveRecoverCap(); got != 1000 {
		t.Fatalf("default liveRecoverCap = %d, want 1000", got)
	}
	var nilCfg *ClientConfig
	if nilCfg.HistoryRecoverRetries() != 2 || nilCfg.HistoryLiveRecoverCap() != 1000 {
		t.Fatal("nil config must fall back to defaults")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	// Old config without the history section backfills to the defaults.
	raw, _ := json.Marshal(map[string]any{"defaults": map[string]any{"username": "A"}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HistoryRecoverRetries() != 2 || c.HistoryLiveRecoverCap() != 1000 {
		t.Fatal("absent history section must backfill to defaults")
	}
	// Explicit values are honored; explicit zero liveRecoverCap means
	// unlimited (returned as 0, not the default).
	raw, _ = json.Marshal(map[string]any{"history": map[string]any{
		"recoverRetries": 5, "recoverRetryDelay": "3s", "liveRecoverCap": 0,
	}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.HistoryRecoverRetries(); got != 5 {
		t.Fatalf("explicit recoverRetries = %d, want 5", got)
	}
	if got := c.HistoryRecoverRetryDelay(); got != 3*time.Second {
		t.Fatalf("explicit recoverRetryDelay = %v, want 3s", got)
	}
	if got := c.HistoryLiveRecoverCap(); got != 0 {
		t.Fatalf("explicit zero liveRecoverCap = %d, want 0 (unlimited)", got)
	}
}

func TestHistoryPagingDefaults(t *testing.T) {
	d := DefaultDynamic()
	if d.HistoryBudgetBurst != 1000 || d.HistoryBudgetPerSec != 500 || d.HistoryRefillMaxRanges != 64 {
		t.Fatalf("history budget defaults = burst %d rate %d ranges %d, want 1000/500/64",
			d.HistoryBudgetBurst, d.HistoryBudgetPerSec, d.HistoryRefillMaxRanges)
	}
	if d.HistoryDiskLookup != 0 {
		t.Fatalf("HistoryDiskLookup = %d, want 0 (RAM-only default)", d.HistoryDiskLookup)
	}
}

func TestNotifyDefaults(t *testing.T) {
	c := DefaultClientConfig()
	if !c.NotifyPow() || !c.NotifyHistory() || !c.NotifyJoin() || !c.NotifyDate() || !c.NotifySystem() {
		t.Fatal("notify gates must default to shown")
	}
	if got := c.NotifyPowMinTier(); got != 1 {
		t.Fatalf("default powMinTier = %d, want 1", got)
	}
	var nilCfg *ClientConfig
	if !nilCfg.NotifyPow() || nilCfg.NotifyPowMinTier() != 1 {
		t.Fatal("nil config must fall back to shown / tier 1")
	}
}

func TestNotifyBackfillAndExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	// Absent notify section backfills to shown.
	raw, _ := json.Marshal(map[string]any{"defaults": map[string]any{"username": "A"}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.NotifyPow() || !c.NotifySystem() {
		t.Fatal("absent notify section must backfill to shown")
	}
	// Explicit false survives the round trip; powMinTier is honored.
	raw, _ = json.Marshal(map[string]any{"ui": map[string]any{"notify": map[string]any{
		"pow": false, "powMinTier": 3, "history": false, "join": false, "date": false, "system": false,
	}}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.NotifyPow() || c.NotifyHistory() || c.NotifyJoin() || c.NotifyDate() || c.NotifySystem() {
		t.Fatal("explicit false must be honored for every gate")
	}
	if got := c.NotifyPowMinTier(); got != 3 {
		t.Fatalf("explicit powMinTier = %d, want 3", got)
	}
}

func TestDefaultAutoVerifyBackfill(t *testing.T) {
	c := DefaultClientConfig()
	if !c.DefaultAutoVerify() {
		t.Fatal("auto-verify must default to enabled")
	}
	var nilCfg *ClientConfig
	if !nilCfg.DefaultAutoVerify() {
		t.Fatal("nil config must fall back to enabled")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	// Absent defaults.autoVerify backfills to enabled.
	raw, _ := json.Marshal(map[string]any{"defaults": map[string]any{"username": "A"}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.DefaultAutoVerify() {
		t.Fatal("absent defaults.autoVerify must backfill to enabled")
	}
	// Explicit false survives.
	raw, _ = json.Marshal(map[string]any{"defaults": map[string]any{"autoVerify": false}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultAutoVerify() {
		t.Fatal("explicit false must be honored")
	}
}
