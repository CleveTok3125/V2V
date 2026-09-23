package main

import (
	"os"
	"testing"
)

// validateEnvKeys are the variables LoadStaticConfig/LoadDynamicConfig
// read. Clearing just these keeps the ambient environment (PATH, HOME)
// intact while letting godotenv.Load populate each key from the fixture,
// and stays safe if tests ever run in parallel.
var validateEnvKeys = []string{
	"INSTANCE_ID", "ONION_HOSTS", "NO_CONTENT_LOGS", "LOG_FILE_PATH",
	"HISTORY_FILE_PATH", "TRUSTED_PROXY_DIR", "REQUIRE_TLS", "PORT",
	"TIMEZONE", "MAX_LOG_SIZE_MB", "MAX_HISTORY_FILE_SIZE_MB", "WEB_ENABLED",
	"ONION_ALLOW_WEB", "ONION_ALLOW_PASSKEY", "PROXY_PROVIDER", "STATUS_URL",
	"DOWNLOAD_URL", "HOMEPAGE_URL", "MAX_CONNECTIONS_PER_IP",
	"MAX_MESSAGE_LENGTH", "MAX_MESSAGE_LINE", "MESSAGE_COOLDOWN",
	"IDLE_CHAT_TIMEOUT", "MAX_HISTORY_BYTES", "MAX_HISTORY_SEND",
	"HISTORY_SEGMENT_COOLDOWN", "HISTORY_DISK_LOOKUP", "MAX_USERNAME_LENGTH",
	"MAX_TRIPCODE_LENGTH", "CONNECTION_COOLDOWN", "ALLOWED_ORIGINS",
	"DATA_DIR",
}

// withCleanEnv unsets the configuration variables for one test and
// restores them afterwards, so godotenv.Load can populate every key from
// the fixture .env without ambient values shadowing it.
func withCleanEnv(t *testing.T) {
	t.Helper()
	old := map[string]string{}
	present := map[string]bool{}
	for _, k := range validateEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			old[k] = v
			present[k] = true
		}
		_ = os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range validateEnvKeys {
			if present[k] {
				_ = os.Setenv(k, old[k])
			} else {
				_ = os.Unsetenv(k)
			}
		}
	})
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

// A well-formed JSON body with a wrong value type passes a map[string]any
// probe but kills the server's LoadRoles at boot, so validate must catch
// it by mirroring the server's role shape.
func TestConfigValidateFailsOnWrongRoleType(t *testing.T) {
	withCleanEnv(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".env":              validValidateEnv,
		"config/roles.json": `{"admin": {"identities": [], "can_message_unlimited": "yes"}}` + "\n",
	})
	cmd := &ConfigValidateCmd{To: root, Format: "text"}
	if err := cmd.Run(); err == nil {
		t.Fatal("wrong role value type must fail validate")
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
