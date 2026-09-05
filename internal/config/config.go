package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// DynamicConfig mirrors server DynamicConfig for reuse on client.
type DynamicConfig struct {
	StatusURL   string `json:"statusUrl"`
	DownloadURL string `json:"downloadUrl"`
	HomepageURL string `json:"homepageUrl"`

	MaxConnectionsPerIP int           `json:"maxConnectionsPerIP"`
	MaxMessageLength    int           `json:"maxMessageLength"`
	MaxMessageLine      int           `json:"maxMessageLine"`
	MessageCooldown     time.Duration `json:"messageCooldown"`
	IdleChatTimeout     time.Duration `json:"idleChatTimeout"`
	MaxHistoryBytes     int           `json:"maxHistoryBytes"`
	MaxHistorySend      int           `json:"maxHistorySend"`
	MaxUsernameLength   int           `json:"maxUsernameLength"`
	MaxTripcodeLength   int           `json:"maxTripcodeLength"`
	ConnectionCooldown  time.Duration `json:"connectionCooldown"`
}

type AppConfig struct {
	Static  StaticConfig
	Dynamic atomic.Pointer[DynamicConfig]
}

type StaticConfig struct {
	Port                 string
	RequireTLS           bool
	AllowedOrigins       []string
	InstanceID           string
	Timezone             *time.Location
	LogFilePath          string
	MaxLogSizeMB         int
	HistoryFilePath      string
	MaxHistoryFileSizeMB int
}

var Cfg AppConfig

var (
	EnvFilePaths   = []string{".env"}
	RolesFilePaths = []string{"./roles.json"}
)

// DefaultDynamic returns defaults matching server template/.env.
func DefaultDynamic() *DynamicConfig {
	return &DynamicConfig{
		StatusURL:           "https://example.com/status",
		DownloadURL:         "https://example.com/download",
		HomepageURL:         "https://example.com/",
		MaxConnectionsPerIP: 2,
		MaxMessageLength:    5000,
		MaxMessageLine:      50,
		MessageCooldown:     200 * time.Millisecond,
		IdleChatTimeout:     30 * time.Minute,
		MaxHistoryBytes:     10485760,
		MaxHistorySend:      500,
		MaxUsernameLength:   12,
		MaxTripcodeLength:   64,
		ConnectionCooldown:  5 * time.Second,
	}
}

// ClientConfig is the full client config.json structure.
type ClientConfig struct {
	Defaults struct {
		Username   string `json:"username"`
		UserAgent  string `json:"userAgent"`
		ShowJoin   bool   `json:"showJoin"`
		AutoVerify bool   `json:"autoVerify"`
	} `json:"defaults"`
	Network struct {
		DefaultScheme string `json:"defaultScheme"`
		DefaultPath   string `json:"defaultPath"`
		AutoUpgrade   bool   `json:"autoUpgrade"`
		WSHandshake   string `json:"wsHandshake"`
		WasmWsOpen    string `json:"wasmWsOpen"`
		WasmPasskey   string `json:"wasmPasskey"`
		ServerInfo    string `json:"serverInfoTimeout"`
	} `json:"network"`
	Limits DynamicConfig `json:"limits"`
	Guard  struct {
		VerifyCooldown string `json:"verifyCooldown"`
		MaxQueryBytes  int    `json:"maxQueryBytes"`
		VerifyMapCap   int    `json:"verifyMapCap"`
		VerifyEntryTTL string `json:"verifyEntryTTL"`
		AuthReadLimit  int    `json:"authReadLimit"`
		MaxRoleLength  int    `json:"maxRoleLength"`
		MaxFails       int    `json:"maxFails"`
		BanDuration    string `json:"banDuration"`
		NonceTTL       string `json:"nonceTTL"`
	} `json:"guard"`
	Channels struct {
		VerifyQueue int `json:"verifyQueue"`
		WasmRecv    int `json:"wasmRecv"`
		WasmKeys    int `json:"wasmKeys"`
		WasmLines   int `json:"wasmLines"`
	} `json:"channels"`
	Crypto struct {
		Argon2 struct {
			Wasm struct {
				T uint32 `json:"t"`
				M uint32 `json:"m"`
				P uint8  `json:"p"`
			} `json:"wasm"`
			Native struct {
				T uint32 `json:"t"`
				M uint32 `json:"m"`
				P uint8  `json:"p"`
			} `json:"native"`
		} `json:"argon2"`
		SaltFallback string `json:"saltFallback"`
		BadgeLen     int    `json:"badgeLen"`
	} `json:"crypto"`
	UI struct {
		Prompts struct {
			Normal    string `json:"normal"`
			Multiline string `json:"multiline"`
			Interrupt string `json:"interrupt"`
			EOF       string `json:"eof"`
		} `json:"prompts"`
		TripPalette [][3]int `json:"tripPalette"`
		// Meta controls the trailing "#height:hash" line. Show is a
		// pointer so an absent section (old config files) backfills to
		// the default instead of silently disabling the line.
		Meta struct {
			Show *bool `json:"show"`
		} `json:"meta"`
		// Mention controls @#height highlights. Color follows the
		// [0,0,0]-means-default convention ([0,0,0] is unreadable on a
		// dark background anyway).
		Mention struct {
			Enabled *bool  `json:"enabled"`
			Color   [3]int `json:"color"`
		} `json:"mention"`
		// Reply controls /reply quotes. QuoteMaxRunes bounds the preview
		// so quotes never wrap and break row math.
		Reply struct {
			Enabled       *bool `json:"enabled"`
			QuoteMaxRunes int   `json:"quoteMaxRunes"`
		} `json:"reply"`
		CodeStyle struct {
			Background [3]int `json:"background"`
			Keyword    [3]int `json:"keyword"`
			String     [3]int `json:"string"`
			Comment    [3]int `json:"comment"`
			Number     [3]int `json:"number"`
			Name       [3]int `json:"name"`
			Function   [3]int `json:"function"`
			Type       [3]int `json:"type"`
			Operator   [3]int `json:"operator"`
		} `json:"codeStyle"`
		Linkify struct {
			TrailingPunct string `json:"trailingPunct"`
		} `json:"linkify"`
		Theme struct {
			Background string `json:"background"`
			Accent     string `json:"accent"`
			Error      string `json:"error"`
		} `json:"theme"`
		Web struct {
			CharAspect int `json:"charAspect"`
			Scrollback int `json:"scrollback"`
		} `json:"web"`
	} `json:"ui"`
	Commands map[string][]string `json:"commands"`
	Tabs struct {
		ChatMaxBytes   int `json:"chatMaxBytes"`
		SystemMaxLines int `json:"systemMaxLines"`
		SystemMaxBytes int `json:"systemMaxBytes"`
	} `json:"tabs"`
	Timeouts struct {
		QuitGrace         string `json:"quitGrace"`
		AuthResponse      string `json:"authResponse"`
		WsPing            string `json:"wsPing"`
		WsPong            string `json:"wsPong"`
		WsWrite           string `json:"wsWrite"`
		ReadHeader        string `json:"readHeader"`
		HistoryFlush      string `json:"historyFlush"`
		WsHandshake       string `json:"wsHandshakeTimeout"`
		EnrollChallengeTTL string `json:"enrollChallengeTTL"`
	} `json:"timeouts"`
}

// DefaultClientConfig returns defaults matching current hardcoded values.
func DefaultClientConfig() *ClientConfig {
	c := &ClientConfig{}
	c.Defaults.Username = "Anonymous"
	c.Defaults.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	c.Defaults.ShowJoin = false
	c.Defaults.AutoVerify = true
	c.Limits = *DefaultDynamic()
	c.Network.DefaultScheme = "wss"
	c.Network.DefaultPath = "/ws"
	c.Network.AutoUpgrade = true
	c.Network.WSHandshake = "45s"
	c.Network.WasmWsOpen = "20s"
	c.Network.WasmPasskey = "70s"
	c.Network.ServerInfo = "5s"
	c.Guard.VerifyCooldown = "200ms"
	c.Guard.MaxQueryBytes = 2048
	c.Guard.VerifyMapCap = 1000
	c.Guard.VerifyEntryTTL = "10m"
	c.Guard.AuthReadLimit = 65536
	c.Guard.MaxRoleLength = 64
	c.Guard.MaxFails = 5
	c.Guard.BanDuration = "5m"
	c.Guard.NonceTTL = "10s"
	c.Channels.VerifyQueue = 128
	c.Channels.WasmRecv = 64
	c.Channels.WasmKeys = 64
	c.Channels.WasmLines = 4
	c.Crypto.Argon2.Wasm.T = 1
	c.Crypto.Argon2.Wasm.M = 32768
	c.Crypto.Argon2.Wasm.P = 1
	c.Crypto.Argon2.Native.T = 3
	c.Crypto.Argon2.Native.M = 65536
	c.Crypto.Argon2.Native.P = 4
	c.Crypto.SaltFallback = "V2V-trip-v1"
	c.Crypto.BadgeLen = 8
	c.UI.Prompts.Normal = "| > "
	c.UI.Prompts.Multiline = "| ... "
	c.UI.Prompts.Interrupt = "^C"
	c.UI.Prompts.EOF = "/quit"
	c.UI.TripPalette = [][3]int{{79, 129, 255}, {129, 199, 132}, {255, 183, 77}, {149, 117, 205}, {77, 208, 225}, {255, 138, 101}, {174, 213, 129}, {144, 164, 174}, {255, 213, 79}, {100, 181, 246}}
	c.UI.Meta.Show = boolPtr(true)
	c.UI.Mention.Enabled = boolPtr(true)
	c.UI.Mention.Color = [3]int{0, 255, 255}
	c.UI.Reply.Enabled = boolPtr(true)
	c.UI.Reply.QuoteMaxRunes = 80
	// Code highlight palette (dark, matching the trip palette hues).
	// A [0,0,0] entry means "use this default".
	c.UI.CodeStyle.Background = [3]int{48, 48, 48}
	c.UI.CodeStyle.Keyword = [3]int{255, 183, 77}
	c.UI.CodeStyle.String = [3]int{174, 213, 129}
	c.UI.CodeStyle.Comment = [3]int{144, 164, 174}
	c.UI.CodeStyle.Number = [3]int{255, 213, 79}
	c.UI.CodeStyle.Name = [3]int{100, 181, 246}
	c.UI.CodeStyle.Function = [3]int{77, 208, 225}
	c.UI.CodeStyle.Type = [3]int{149, 117, 205}
	c.UI.CodeStyle.Operator = [3]int{216, 222, 233}
	c.UI.Linkify.TrailingPunct = ".,;:!?)»\"'’\"…"
	c.UI.Theme.Background = "#101014"
	c.UI.Theme.Accent = "#4f7dff"
	c.UI.Theme.Error = "#ff6b6b"
	c.UI.Web.CharAspect = 0
	c.UI.Web.Scrollback = 10000
	c.Commands = map[string][]string{
		"quit":         {"/quit", "/q"},
		"clear":        {"/clear", "/c"},
		"clearHistory": {"/clearhistory", "/ch"},
		"showJoin":     {"/showjoin", "/sj"},
		"autoVerify":   {"/autoverify", "/av"},
		"info":         {"/info"},
		"tab":          {"/tab", "/t"},
		"meta":         {"/meta", "/m"},
		"find":         {"/find", "/f"},
		"reply":        {"/reply"},
		"whoami":       {"/whoami", "/w"},
		"status":       {"/status"},
		"help":         {"/help", "/h"},
	}
	c.Tabs.ChatMaxBytes = 2097152
	c.Tabs.SystemMaxLines = 2000
	c.Tabs.SystemMaxBytes = 409600
	c.Timeouts.QuitGrace = "500ms"
	c.Timeouts.AuthResponse = "12s"
	c.Timeouts.WsPing = "50s"
	c.Timeouts.WsPong = "60s"
	c.Timeouts.WsWrite = "10s"
	c.Timeouts.ReadHeader = "5s"
	c.Timeouts.HistoryFlush = "1s"
	c.Timeouts.WsHandshake = "45s"
	c.Timeouts.EnrollChallengeTTL = "5m"
	return c
}

// boolPtr allocates a bool for default config values that must tell
// "absent" apart from "false" after JSON round-trips.
func boolPtr(v bool) *bool { return &v }

// ShowMeta reports whether message meta lines render. A nil pointer
// (hand-edited or ancient config) means the default: shown.
func (c *ClientConfig) ShowMeta() bool {
	if c == nil || c.UI.Meta.Show == nil {
		return true
	}
	return *c.UI.Meta.Show
}

// MentionEnabled reports whether @#height mentions highlight.
func (c *ClientConfig) MentionEnabled() bool {
	if c == nil || c.UI.Mention.Enabled == nil {
		return true
	}
	return *c.UI.Mention.Enabled
}

// MentionColor returns the clamped highlight triple, defaulting bright
// cyan for absent ([0,0,0]) or out-of-range channels.
func (c *ClientConfig) MentionColor() [3]int {
	def := [3]int{0, 255, 255}
	if c == nil {
		return def
	}
	v := c.UI.Mention.Color
	if v == ([3]int{}) {
		return def
	}
	for i, ch := range v {
		if ch < 0 {
			v[i] = 0
		} else if ch > 255 {
			v[i] = 255
		}
	}
	return v
}

// ReplyEnabled reports whether /reply quotes send.
func (c *ClientConfig) ReplyEnabled() bool {
	if c == nil || c.UI.Reply.Enabled == nil {
		return true
	}
	return *c.UI.Reply.Enabled
}

// QuoteMaxRunes bounds reply preview length (clamped 20-200, default 80)
// so quotes stay single-line on narrow screens.
func (c *ClientConfig) QuoteMaxRunes() int {
	if c == nil || c.UI.Reply.QuoteMaxRunes <= 0 {
		return 80
	}
	if c.UI.Reply.QuoteMaxRunes < 20 {
		return 20
	}
	if c.UI.Reply.QuoteMaxRunes > 200 {
		return 200
	}
	return c.UI.Reply.QuoteMaxRunes
}

// LoadOrCreate loads config from path, creates default if missing when autoCreate is true.
func LoadOrCreate(path string, autoCreate bool) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && autoCreate {
			c := DefaultClientConfig()
			if err := Save(path, c); err != nil {
				return nil, err
			}
			return c, nil
		}
		if os.IsNotExist(err) {
			return DefaultClientConfig(), nil
		}
		return nil, err
	}
	var c ClientConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	// Backfill tab caps for config files written before the tabs section existed.
	def := DefaultClientConfig()
	// Backfill numeric limits: a partial config must never zero out the
	// send guards (a zero MaxMessageLength would reject every message).
	if c.Limits.MaxConnectionsPerIP <= 0 {
		c.Limits.MaxConnectionsPerIP = def.Limits.MaxConnectionsPerIP
	}
	if c.Limits.MaxMessageLength <= 0 {
		c.Limits.MaxMessageLength = def.Limits.MaxMessageLength
	}
	if c.Limits.MaxMessageLine <= 0 {
		c.Limits.MaxMessageLine = def.Limits.MaxMessageLine
	}
	if c.Limits.MessageCooldown <= 0 {
		c.Limits.MessageCooldown = def.Limits.MessageCooldown
	}
	if c.Limits.IdleChatTimeout <= 0 {
		c.Limits.IdleChatTimeout = def.Limits.IdleChatTimeout
	}
	if c.Limits.MaxHistoryBytes <= 0 {
		c.Limits.MaxHistoryBytes = def.Limits.MaxHistoryBytes
	}
	if c.Limits.MaxHistorySend <= 0 {
		c.Limits.MaxHistorySend = def.Limits.MaxHistorySend
	}
	if c.Limits.MaxUsernameLength <= 0 {
		c.Limits.MaxUsernameLength = def.Limits.MaxUsernameLength
	}
	if c.Limits.MaxTripcodeLength <= 0 {
		c.Limits.MaxTripcodeLength = def.Limits.MaxTripcodeLength
	}
	if c.Limits.ConnectionCooldown <= 0 {
		c.Limits.ConnectionCooldown = def.Limits.ConnectionCooldown
	}
	// Backfill meta visibility (absent section means default: shown).
	if c.UI.Meta.Show == nil {
		c.UI.Meta.Show = def.UI.Meta.Show
	}
	// Backfill mention/reply knobs the same way.
	if c.UI.Mention.Enabled == nil {
		c.UI.Mention.Enabled = def.UI.Mention.Enabled
	}
	if c.UI.Mention.Color == ([3]int{}) {
		c.UI.Mention.Color = def.UI.Mention.Color
	}
	if c.UI.Reply.Enabled == nil {
		c.UI.Reply.Enabled = def.UI.Reply.Enabled
	}
	if c.UI.Reply.QuoteMaxRunes <= 0 {
		c.UI.Reply.QuoteMaxRunes = def.UI.Reply.QuoteMaxRunes
	}
	if c.Tabs.ChatMaxBytes <= 0 {
		c.Tabs.ChatMaxBytes = def.Tabs.ChatMaxBytes
	}
	if c.Tabs.SystemMaxLines <= 0 {
		c.Tabs.SystemMaxLines = def.Tabs.SystemMaxLines
	}
	if c.Tabs.SystemMaxBytes <= 0 {
		c.Tabs.SystemMaxBytes = def.Tabs.SystemMaxBytes
	}
	// Backfill code highlight palette ([0,0,0] means "use default").
	if c.UI.CodeStyle.Background == ([3]int{}) {
		c.UI.CodeStyle.Background = def.UI.CodeStyle.Background
	}
	if c.UI.CodeStyle.Keyword == ([3]int{}) {
		c.UI.CodeStyle.Keyword = def.UI.CodeStyle.Keyword
	}
	if c.UI.CodeStyle.String == ([3]int{}) {
		c.UI.CodeStyle.String = def.UI.CodeStyle.String
	}
	if c.UI.CodeStyle.Comment == ([3]int{}) {
		c.UI.CodeStyle.Comment = def.UI.CodeStyle.Comment
	}
	if c.UI.CodeStyle.Number == ([3]int{}) {
		c.UI.CodeStyle.Number = def.UI.CodeStyle.Number
	}
	if c.UI.CodeStyle.Name == ([3]int{}) {
		c.UI.CodeStyle.Name = def.UI.CodeStyle.Name
	}
	if c.UI.CodeStyle.Function == ([3]int{}) {
		c.UI.CodeStyle.Function = def.UI.CodeStyle.Function
	}
	if c.UI.CodeStyle.Type == ([3]int{}) {
		c.UI.CodeStyle.Type = def.UI.CodeStyle.Type
	}
	if c.UI.CodeStyle.Operator == ([3]int{}) {
		c.UI.CodeStyle.Operator = def.UI.CodeStyle.Operator
	}
	return &c, nil
}

// Save writes config atomically.
func Save(path string, c *ClientConfig) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := filepath.Join(dir, ".tmp-config.json")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
