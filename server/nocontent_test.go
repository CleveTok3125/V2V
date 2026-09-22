package main

import "testing"

func TestEffectiveStoragePaths(t *testing.T) {
	logPath, histPath := effectiveStoragePaths(false, "app.log", "history.jsonl")
	if logPath != "app.log" || histPath != "history.jsonl" {
		t.Fatalf("default policy must keep configured paths: %q %q", logPath, histPath)
	}
	logPath, histPath = effectiveStoragePaths(true, "app.log", "history.jsonl")
	if logPath != "" || histPath != "" {
		t.Fatalf("NO_CONTENT_LOGS must clear both paths: %q %q", logPath, histPath)
	}
}

func TestContentLoggingEnabled(t *testing.T) {
	testCfg(t)
	old := Cfg.Static.NoContentLogs
	t.Cleanup(func() { Cfg.Static.NoContentLogs = old })

	Cfg.Static.NoContentLogs = false
	if !contentLoggingEnabled() {
		t.Fatal("content logging must be on by default")
	}
	Cfg.Static.NoContentLogs = true
	if contentLoggingEnabled() {
		t.Fatal("NO_CONTENT_LOGS must disable content logging")
	}
}

func TestInitHistoryStoreRAMOnly(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	if err := s.InitHistoryStore("", 0); err != nil {
		t.Fatal(err)
	}
	if s.Chain.Store != nil {
		t.Fatal("empty history path must disable the disk store")
	}
	s.Chain.appendMessageToHistory(`{"type":"chat"}`)
	s.Chain.Mu.RLock()
	n := len(s.Chain.History)
	s.Chain.Mu.RUnlock()
	if n != 1 {
		t.Fatalf("RAM-only history must still accept appends, got %d", n)
	}
}
