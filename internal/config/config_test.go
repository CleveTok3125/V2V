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
