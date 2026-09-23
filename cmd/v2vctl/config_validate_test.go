package main

import (
	"os"
	"strings"
	"testing"
)

// withCleanEnv empties the process environment for one test and restores it
// afterwards, so godotenv.Load can populate every key from the fixture .env
// without ambient values shadowing it.
func withCleanEnv(t *testing.T) {
	t.Helper()
	old := os.Environ()
	t.Cleanup(func() {
		os.Clearenv()
		for _, kv := range old {
			if i := strings.IndexByte(kv, '='); i >= 0 {
				_ = os.Setenv(kv[:i], kv[i+1:])
			}
		}
	})
	os.Clearenv()
}

const validValidateEnv = "PORT=10000\n" +
	"PROXY_PROVIDER=none\n" +
	"MAX_LOG_SIZE_MB=50\n" +
	"MAX_HISTORY_FILE_SIZE_MB=50\n" +
	"MAX_CONNECTIONS_PER_IP=2\n" +
	"MAX_MESSAGE_LENGTH=5000\n" +
	"MAX_MESSAGE_LINE=50\n" +
	"MESSAGE_COOLDOWN=200ms\n" +
	"IDLE_CHAT_TIMEOUT=30m\n" +
	"MAX_HISTORY_BYTES=10485760\n" +
	"MAX_HISTORY_SEND=500\n" +
	"HISTORY_SEGMENT_COOLDOWN=2s\n" +
	"HISTORY_DISK_LOOKUP=0\n" +
	"MAX_USERNAME_LENGTH=12\n" +
	"CONNECTION_COOLDOWN=5s\n"

func TestConfigValidatePass(t *testing.T) {
	withCleanEnv(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".env":              validValidateEnv,
		"config/roles.json": "{}\n",
	})
	cmd := &ConfigValidateCmd{To: root, Format: "json"}
	if err := cmd.Run(); err != nil {
		t.Fatalf("valid instance must pass: %v", err)
	}
}

func TestConfigValidateFailsOnProxyProvider(t *testing.T) {
	withCleanEnv(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".env": "PORT=10000\nPROXY_PROVIDER=false\n" +
			"MAX_LOG_SIZE_MB=50\nMAX_HISTORY_FILE_SIZE_MB=50\n",
		"config/roles.json": "{}\n",
	})
	cmd := &ConfigValidateCmd{To: root, Format: "json"}
	if err := cmd.Run(); err == nil {
		t.Fatal("PROXY_PROVIDER=false must fail validate")
	}
}

func TestConfigValidateFailsOnBadRoles(t *testing.T) {
	withCleanEnv(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".env":              validValidateEnv,
		"config/roles.json": "{ this is not json",
	})
	cmd := &ConfigValidateCmd{To: root, Format: "text"}
	if err := cmd.Run(); err == nil {
		t.Fatal("malformed roles.json must fail validate")
	}
}

func TestConfigValidateFailsOnMissingEnv(t *testing.T) {
	withCleanEnv(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"config/roles.json": "{}\n",
	})
	cmd := &ConfigValidateCmd{To: root, Format: "text"}
	if err := cmd.Run(); err == nil {
		t.Fatal("missing .env must fail validate")
	}
}
