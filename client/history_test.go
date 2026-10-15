package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryLoadAddAndRecall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.tmp")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := loadHistory(path)
	if len(h.lines) != 2 || h.lines[0] != "one" || h.lines[1] != "two" {
		t.Fatalf("load = %v", h.lines)
	}

	if v, ok := h.prev("draft"); !ok || v != "two" {
		t.Fatalf("prev 1 = %q, %v", v, ok)
	}
	if v, ok := h.prev(""); !ok || v != "one" {
		t.Fatalf("prev 2 = %q, %v", v, ok)
	}
	if _, ok := h.prev(""); ok {
		t.Fatal("prev past the oldest entry must fail")
	}
	if v, ok := h.next(); !ok || v != "two" {
		t.Fatalf("next 1 = %q, %v", v, ok)
	}
	if v, ok := h.next(); !ok || v != "draft" {
		t.Fatalf("next must restore the stashed draft, got %q, %v", v, ok)
	}
	if _, ok := h.next(); ok {
		t.Fatal("next past the draft must fail")
	}
}

func TestHistoryAddDedupesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.tmp")
	h := loadHistory(path)
	h.add("a")
	h.add("a") // immediate duplicate is not stored twice
	h.add("b")
	if len(h.lines) != 2 {
		t.Fatalf("lines = %v", h.lines)
	}
	if h.idx != len(h.lines) {
		t.Fatalf("idx = %d, want %d", h.idx, len(h.lines))
	}
	reloaded := loadHistory(path)
	if len(reloaded.lines) != 2 || reloaded.lines[0] != "a" || reloaded.lines[1] != "b" {
		t.Fatalf("reload = %v", reloaded.lines)
	}
}

func TestHistoryFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.tmp")
	// A permissive umask created file must be tightened on save.
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := loadHistory(path)
	h.add("y")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestHistoryEmptyPathIsMemoryOnly(t *testing.T) {
	h := loadHistory("")
	h.add("x")
	if len(h.lines) != 1 || h.idx != 1 {
		t.Fatalf("memory-only history = %v (idx %d)", h.lines, h.idx)
	}
}

func TestOlderAvailableAndRequest(t *testing.T) {
	sess := NewSession()
	sess.Chain.WireIdx = newWireIndex(16)
	if sess.olderAvailable() {
		t.Fatal("empty index must report no older history")
	}
	if ok, _ := sess.requestOlder(50); ok {
		t.Fatal("empty index must not send a request")
	}

	sess.Conn = &stubConn{}
	sess.Chain.WireIdx.put(WireMessage{ChainHeight: 5})
	if !sess.olderAvailable() {
		t.Fatal("a wire at height 5 must report older history")
	}
	if ok, err := sess.requestOlder(50); !ok || err != nil {
		t.Fatalf("requestOlder = %v, %v", ok, err)
	}

	sess.Chain.WireIdx.put(WireMessage{ChainHeight: 1})
	if sess.olderAvailable() {
		t.Fatal("oldest at height 1 must report no older history")
	}

	sess.Chain.WireIdx = newWireIndex(16)
	sess.Chain.WireIdx.put(WireMessage{ChainHeight: 9})
	sess.Chain.InOlder = true
	if sess.olderAvailable() {
		t.Fatal("an in-flight older segment must suppress a new request")
	}
}
