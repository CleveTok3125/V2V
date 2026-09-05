package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadOrCreate(path, false)
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
	c, err = LoadOrCreate(path, false)
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
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadOrCreate(path, false)
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
	path := filepath.Join(dir, "config.json")
	raw, _ := json.Marshal(map[string]any{"ui": map[string]any{
		"mention": map[string]any{"enabled": false, "color": []int{300, -5, 128}},
		"reply":   map[string]any{"quoteMaxRunes": 5000},
	}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadOrCreate(path, false)
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

func TestClipboardBackfill(t *testing.T) {
	dir := t.TempDir()
	old := map[string]any{"defaults": map[string]any{"username": "A"}}
	data, _ := json.Marshal(old)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadOrCreate(path, false)
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
	c, err = LoadOrCreate(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.ClipboardClearAfterSec() != 0 {
		t.Fatal("explicit 0 must survive backfill (disable)")
	}
}
