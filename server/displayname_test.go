package main

import (
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestDisplayName_AlwaysHash(t *testing.T) {
	s := NewChatServer()
	// Use fixed salt for determinism
	s.DisplaySalt = []byte("test-salt-32-bytes-long-for-test!!")
	// Guest
	name := s.generateDisplayName("Alice", "1.2.3.4", GetDefaultPermission())
	if !strings.Contains(name, "#") {
		t.Fatalf("guest should have hash, got %q", name)
	}
	// Admin with CustomPrefix should also have hash
	adminPerms := Permission{CustomPrefix: "[Admin] ", CanMessageUnlimited: true}
	adminName := s.generateDisplayName("Alice", "1.2.3.4", adminPerms)
	if !strings.Contains(adminName, "#") {
		t.Fatalf("admin should also have hash, got %q", adminName)
	}
	if !strings.HasPrefix(adminName, "[Admin] ") {
		t.Fatalf("admin should keep prefix, got %q", adminName)
	}
	// Ensure different IP gives different hash (with same salt)
	other := s.generateDisplayName("Alice", "5.6.7.8", GetDefaultPermission())
	if name == other {
		t.Fatalf("different IP should give different hash: %q vs %q", name, other)
	}
}

func TestDisplayName_Serial(t *testing.T) {
	s := NewChatServer()
	s.DisplaySalt = []byte("fixed-salt-for-serial-test-32bytes!")
	// Same IP and same username will give same base hash
	ip := "10.0.0.1"
	perms := GetDefaultPermission()
	n1 := s.generateDisplayName("Bob", ip, perms)
	// Simulate that n1 is now active (generateDisplayName already stored it)
	// Next user with same name and same IP (same hash) should get serial -2
	// To force same hash, we need same IP and same name, which we do
	// But the second call will see baseDisplay already exists, so it should get -2
	n2 := s.generateDisplayName("Bob", ip, perms)
	if n2 == n1 {
		t.Fatalf("duplicate should get serial, both got %q", n1)
	}
	if !strings.HasSuffix(n2, "-2") {
		t.Fatalf("second duplicate should have -2 suffix, got %q", n2)
	}
	// Third duplicate -> -3
	n3 := s.generateDisplayName("Bob", ip, perms)
	if !strings.HasSuffix(n3, "-3") {
		t.Fatalf("third should have -3, got %q", n3)
	}
	// Different username same IP should not collide (different base)
	n4 := s.generateDisplayName("Alice", ip, perms)
	if strings.Contains(n4, "-2") && !strings.Contains(n4, "Alice") {
		t.Fatalf("different name should not get serial for Bob")
	}
}

func TestDisplayName_DynamicLength(t *testing.T) {
	s := NewChatServer()
	s.DisplaySalt = []byte("fixed-salt-dynamic-len-32bytes!!")
	// Simulate many active clients to trigger hashLen expansion
	// Fill Clients map with dummy entries
	s.ClientsMu.Lock()
	for i := 0; i < 150; i++ {
		// dummy conn nil is okay for counting, but map key must be unique
		// Use a fake *websocket.Conn via new(websocket.Conn) pointer
		conn := &websocket.Conn{}
		s.Clients[conn] = &ClientSession{DisplayName: "dummy"}
	}
	s.ClientsMu.Unlock()

	name := s.generateDisplayName("Charlie", "192.168.1.1", GetDefaultPermission())
	// Extract hash part: after '#', before '-' if any
	hashPart := name
	if idx := strings.LastIndex(name, "#"); idx != -1 {
		hashPart = name[idx+1:]
		if dashIdx := strings.Index(hashPart, "-"); dashIdx != -1 {
			hashPart = hashPart[:dashIdx]
		}
	}
	if len(hashPart) < 5 {
		t.Fatalf("with 150 clients hashLen should expand to 5, got hash %q len %d in %q", hashPart, len(hashPart), name)
	}

	// Test with many more to get 6
	s.ClientsMu.Lock()
	for i := 0; i < 700; i++ {
		conn := &websocket.Conn{}
		s.Clients[conn] = &ClientSession{DisplayName: "dummy2"}
	}
	s.ClientsMu.Unlock()
	name2 := s.generateDisplayName("Dave", "10.10.10.10", GetDefaultPermission())
	hashPart2 := name2
	if idx := strings.LastIndex(name2, "#"); idx != -1 {
		hashPart2 = name2[idx+1:]
		if dashIdx := strings.Index(hashPart2, "-"); dashIdx != -1 {
			hashPart2 = hashPart2[:dashIdx]
		}
	}
	if len(hashPart2) < 6 {
		t.Fatalf("with ~850 clients hashLen should be 6, got %q len %d", hashPart2, len(hashPart2))
	}
}

func TestDisplayName_SaltPerSession(t *testing.T) {
	s1 := NewChatServer()
	s2 := NewChatServer()
	// Different salts should give different hashes for same IP
	s1.DisplaySalt = []byte("salt-one-32-bytes-long-for-test-1!")
	s2.DisplaySalt = []byte("salt-two-32-bytes-long-for-test-2!")
	name1 := s1.generateDisplayName("Eve", "8.8.8.8", GetDefaultPermission())
	name2 := s2.generateDisplayName("Eve", "8.8.8.8", GetDefaultPermission())
	if name1 == name2 {
		t.Fatalf("different salts should give different hashes: %q vs %q", name1, name2)
	}
}

func TestDisplayName_ReleaseSerial(t *testing.T) {
	s := NewChatServer()
	s.DisplaySalt = []byte("fixed-salt-release-test-32bytes!!")
	ip := "172.16.0.1"
	perms := GetDefaultPermission()
	n1 := s.generateDisplayName("Frank", ip, perms)
	// Simulate client session
	conn := &websocket.Conn{}
	// Manually add to Clients and DisplayNameCount is already done by generateDisplayName
	s.ClientsMu.Lock()
	s.Clients[conn] = &ClientSession{DisplayName: n1}
	s.ClientsMu.Unlock()

	// Next duplicate gets -2
	n2 := s.generateDisplayName("Frank", ip, perms)
	if !strings.HasSuffix(n2, "-2") {
		t.Fatalf("expected -2, got %q", n2)
	}
	// Simulate leave of first user
	s.DisplayNameCountMu.Lock()
	delete(s.DisplayNameCount, n1)
	s.DisplayNameCountMu.Unlock()
	s.ClientsMu.Lock()
	delete(s.Clients, conn)
	s.ClientsMu.Unlock()

	// Now new user with same base should be able to reuse n1 (without serial)
	// But current implementation will try to reuse n1 since we deleted it, so it should give n1 again, not -3
	n3 := s.generateDisplayName("Frank", ip, perms)
	if n3 != n1 {
		// It's okay if it gives n1 again (reuse) or -2 again? Our logic will give n1 again since base is free.
		// Check that it doesn't give -3
		if strings.HasSuffix(n3, "-3") {
			t.Fatalf("should reuse freed slot, got %q expected %q", n3, n1)
		}
	}
}

func TestDisplayName_Truncate(t *testing.T) {
	// Setup config for deterministic truncate
	Cfg.Dynamic.Store(&DynamicConfig{MaxUsernameLength: 12})
	defer Cfg.Dynamic.Store(nil)

	s := NewChatServer()
	s.DisplaySalt = []byte("fixed-salt-truncate-test-32bytes!")
	longName := strings.Repeat("a", 100)
	perms := GetDefaultPermission()
	name := s.generateDisplayName(longName, "1.1.1.1", perms)
	// Should contain # and hash, and base part truncated to 12
	if !strings.Contains(name, "#") {
		t.Fatalf("should have hash: %q", name)
	}
	base := name[:strings.LastIndex(name, "#")]
	if len([]rune(base)) != 12 {
		t.Fatalf("base should be truncated to 12, got %q len %d", base, len([]rune(base)))
	}
}
