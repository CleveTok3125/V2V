package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

func TestLoadBlocklistMissing(t *testing.T) {
	nets, warn, err := loadBlocklist(filepath.Join(t.TempDir(), "nope.txt"))
	if err != nil {
		t.Fatalf("missing file must be tolerated, got %v", err)
	}
	if nets != nil {
		t.Fatalf("missing file must yield no nets, got %v", nets)
	}
	if warn == "" {
		t.Fatal("missing file must warn")
	}
}

func TestLoadBlocklistMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocklist.txt")
	body := "# tor exits\n203.0.113.0/24\n2001:db8::1\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	nets, warn, err := loadBlocklist(path)
	if err != nil {
		t.Fatal(err)
	}
	if warn != "" {
		t.Fatalf("valid file must not warn: %s", warn)
	}
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"203.0.113.7", true},
		{"203.0.114.7", false},
		{"2001:db8::1", true},
		{"2001:db8::2", false},
		{"not-an-ip", false},
	} {
		if got := blocklisted(nets, tc.ip); got != tc.want {
			t.Fatalf("blocklisted(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestLoadBlocklistMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocklist.txt")
	if err := os.WriteFile(path, []byte("999.999.0.0/99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBlocklist(path); err == nil {
		t.Fatal("malformed file must fail")
	}
}

func TestLoadBlocklistWorldWritable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocklist.txt")
	if err := os.WriteFile(path, []byte("10.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBlocklist(path); err == nil {
		t.Fatal("world-writable file must fail")
	}
}

func TestIsIPv4(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"::ffff:192.0.2.1", true},
		{"::1", false},
		{"2001:db8::1", false},
		{"garbage", false},
	} {
		if got := isIPv4(tc.ip); got != tc.want {
			t.Fatalf("isIPv4(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func abuseConfigForTest(maxTotal int) *serverconfig.AbuseConfig {
	return &serverconfig.AbuseConfig{MaxTotalConnections: maxTotal}
}

func TestOverCap(t *testing.T) {
	old := Cfg.Abuse.Load()
	defer Cfg.Abuse.Store(old)
	Cfg.Abuse.Store(abuseConfigForTest(2))
	s := NewChatServer()
	if s.overCap() {
		t.Fatal("empty server must not be over cap")
	}
	s.Hub.Clients[&websocket.Conn{}] = nil
	s.Hub.Clients[&websocket.Conn{}] = nil
	if !s.overCap() {
		t.Fatal("2 clients with cap 2 must be over cap")
	}
	Cfg.Abuse.Store(nil)
	if s.overCap() {
		t.Fatal("nil abuse config must disable the cap")
	}
}
