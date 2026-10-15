package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/CleveTok3125/V2V/lib/identity"
)

// ErrEncrypted signals a passphrase-sealed config. Retry with LoadEncrypted.
var ErrEncrypted = errors.New("config file is encrypted")

// DynamicConfig mirrors the server limits for client reuse.
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
	// HistoryBudgetBurst/PerSec bound history cost as a token bucket:
	// each request spends its line count, refilled at PerSec up to Burst.
	HistoryBudgetBurst     int `json:"historyBudgetBurst"`
	HistoryBudgetPerSec    int `json:"historyBudgetPerSec"`
	HistoryRefillMaxRanges int `json:"historyRefillMaxRanges"`
	// HistoryReplayBatchLines/Bytes coalesce a replay into multi-line
	// frames so a bounded window fits the per-session send queue.
	HistoryReplayBatchLines int `json:"historyReplayBatchLines"`
	HistoryReplayBatchBytes int `json:"historyReplayBatchBytes"`
	// HistoryDiskLookup tiers on-demand older-segment reads: 0 RAM-only,
	// 1 +active history.jsonl, 2 +.old raw, 3 +.old.zst archive.
	HistoryDiskLookup  int           `json:"historyDiskLookup"`
	MaxUsernameLength  int           `json:"maxUsernameLength"`
	MaxTripcodeLength  int           `json:"maxTripcodeLength"`
	ConnectionCooldown time.Duration `json:"connectionCooldown"`
}

// DefaultDynamic returns defaults matching the server instance template.
func DefaultDynamic() *DynamicConfig {
	return &DynamicConfig{
		StatusURL:               "https://example.com/status",
		DownloadURL:             "https://example.com/download",
		HomepageURL:             "https://example.com/",
		MaxConnectionsPerIP:     2,
		MaxMessageLength:        5000,
		MaxMessageLine:          50,
		MessageCooldown:         1000 * time.Millisecond,
		IdleChatTimeout:         30 * time.Minute,
		MaxHistoryBytes:         10485760,
		MaxHistorySend:          500,
		HistoryBudgetBurst:      1000,
		HistoryBudgetPerSec:     500,
		HistoryRefillMaxRanges:  64,
		HistoryReplayBatchLines: 32,
		HistoryReplayBatchBytes: 16384,
		HistoryDiskLookup:       0,
		MaxUsernameLength:       12,
		MaxTripcodeLength:       64,
		ConnectionCooldown:      5 * time.Second,
	}
}

// ClientConfig is the full client config.jsonc structure.
type ClientConfig struct {
	Defaults struct {
		Username  string `json:"username"`
		UserAgent string `json:"userAgent"`
		ShowJoin  bool   `json:"showJoin"`
		// AutoVerify is a pointer so an omitted value backfills to enabled.
		AutoVerify *bool `json:"autoVerify"`
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
		MessageCooldown     string `json:"messageCooldown"`
		ConnectionCooldown  string `json:"connectionCooldown"`
		IdleTimeout         string `json:"idleTimeout"`
		AuthResponseTimeout string `json:"authResponseTimeout"`
		VerifyCooldown      string `json:"tripVerifyCooldown"`
		MaxQueryBytes       int    `json:"maxQueryBytes"`
		VerifyMapCap        int    `json:"verifyMapCap"`
		VerifyEntryTTL      string `json:"verifyEntryTTL"`
		AuthReadLimit       int    `json:"authReadLimit"`
		MaxRoleLength       int    `json:"maxRoleLength"`
		MaxFails            int    `json:"maxFails"`
		BanDuration         string `json:"banDuration"`
		NonceTTL            string `json:"nonceTTL"`
		MaxConnectionsPerIP int    `json:"maxConnectionsPerIP"`
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
		// Meta Show is a pointer so old files without this section
		// backfill to shown.
		Meta struct {
			Show *bool `json:"show"`
		} `json:"meta"`
		// Mention Color uses the [0,0,0]-means-default convention.
		Mention struct {
			Enabled *bool  `json:"enabled"`
			Color   [3]int `json:"color"`
		} `json:"mention"`
		// Reply QuoteMaxRunes bounds the preview so quotes never wrap
		// and break row math.
		Reply struct {
			Enabled       *bool `json:"enabled"`
			QuoteMaxRunes int   `json:"quoteMaxRunes"`
		} `json:"reply"`
		// Clipboard ClearAfterSec is a pointer so old files without this
		// section backfill to the default instead of disabling clearing.
		Clipboard struct {
			ClearAfterSec *int `json:"clearAfterSec"`
		} `json:"clipboard"`
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
			CharAspect float64 `json:"charAspect"`
			TargetCols struct {
				Coarse  int `json:"coarse"`
				Desktop int `json:"desktop"`
				Narrow  int `json:"narrow"`
			} `json:"targetCols"`
			Scrollback     int    `json:"scrollback"`
			ResizeDebounce string `json:"resizeDebounce"`
		} `json:"web"`
		// History SaveMessages is a pointer so old files without this
		// section keep the default (off): slash commands persist, chat
		// text never lands on disk.
		History struct {
			SaveMessages *bool `json:"saveMessages"`
		} `json:"history"`
		// Collapse Rows is the fold threshold in screen rows, PreviewRows
		// the surviving head rows. Enabled is a pointer so old files
		// backfill to enabled.
		Collapse struct {
			Enabled     *bool `json:"enabled"`
			Rows        int   `json:"rows"`
			PreviewRows int   `json:"previewRows"`
		} `json:"collapse"`
		// VersionCheck Mode is disabled|warn|enforce (default warn);
		// empty Expect compares against this client's own stamp.
		VersionCheck struct {
			Enabled *bool  `json:"enabled"`
			Mode    string `json:"mode"`
			Expect  string `json:"expect"`
		} `json:"versionCheck"`
		// Notify gates notices by tag: false mutes the live line while
		// it still lands in the system tab. Critical warnings (chain
		// integrity, send guards, connection loss, command output) are
		// never gated. Keys are the tag paths the server publishes;
		// muting a node hides its subtree. An absent key means shown.
		Notify NotifyGates `json:"notify"`
		// PowMinTier hides the "solving PoW" notice below it (default 1).
		PowMinTier *int `json:"powMinTier"`
	} `json:"ui"`
	Commands map[string][]string `json:"commands"`
	Tabs     struct {
		ChatMaxBytes   int `json:"chatMaxBytes"`
		SystemMaxLines int `json:"systemMaxLines"`
		SystemMaxBytes int `json:"systemMaxBytes"`
	} `json:"tabs"`
	Timeouts struct {
		QuitGrace          string `json:"quitGrace"`
		AuthResponse       string `json:"authResponse"`
		WsPing             string `json:"wsPing"`
		WsPong             string `json:"wsPong"`
		WsWrite            string `json:"wsWrite"`
		ReadHeader         string `json:"readHeader"`
		HistoryFlush       string `json:"historyFlush"`
		WsHandshake        string `json:"wsHandshake"`
		EnrollChallengeTTL string `json:"enrollChallengeTTL"`
	} `json:"timeouts"`
	// Pow MaxTier/MaxCostMs bound client work: offers above either are
	// declined, so a malicious server cannot burn the machine.
	Pow struct {
		MaxTier   int   `json:"maxTier"`
		MaxCostMs int64 `json:"maxCostMs"`
	} `json:"pow"`
	// History bounds recovery of missed lines. RecoverRetryDelay must
	// cover the server history budget refill. LiveRecoverCap bounds
	// lines auto-refilled from live gaps per session (0 = unlimited).
	History struct {
		InitialLines      *int    `json:"initialLines"`
		BatchLines        *int    `json:"batchLines"`
		RecoverRetries    *int    `json:"recoverRetries"`
		RecoverRetryDelay *string `json:"recoverRetryDelay"`
		LiveRecoverCap    *int    `json:"liveRecoverCap"`
	} `json:"history"`
}

// History recovery defaults for an absent history section.
const (
	DefaultHistoryInitialLines      = 500
	DefaultHistoryBatchLines        = 100
	DefaultHistoryRecoverRetries    = 2
	DefaultHistoryRecoverRetryDelay = 2 * time.Second
	DefaultHistoryLiveRecoverCap    = 1000
)

// DefaultClientConfig returns defaults matching current hardcoded values.
func DefaultClientConfig() *ClientConfig {
	c := &ClientConfig{}
	c.Defaults.Username = "Anonymous"
	c.Defaults.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	c.Defaults.ShowJoin = false
	c.Defaults.AutoVerify = boolPtr(true)
	c.Limits = *DefaultDynamic()
	c.Network.DefaultScheme = "wss"
	c.Network.DefaultPath = "/ws"
	c.Network.AutoUpgrade = true
	c.Network.WSHandshake = "45s"
	c.Network.WasmWsOpen = "20s"
	c.Network.WasmPasskey = "70s"
	c.Network.ServerInfo = "5s"
	c.Guard.MessageCooldown = "1000ms"
	c.Guard.ConnectionCooldown = "5s"
	c.Guard.IdleTimeout = "30m"
	c.Guard.AuthResponseTimeout = "12s"
	c.Guard.VerifyCooldown = "200ms"
	c.Guard.MaxQueryBytes = 2048
	c.Guard.VerifyMapCap = 1000
	c.Guard.VerifyEntryTTL = "10m"
	c.Guard.AuthReadLimit = 65536
	c.Guard.MaxRoleLength = 64
	c.Guard.MaxFails = 5
	c.Guard.BanDuration = "5m"
	c.Guard.NonceTTL = "10s"
	c.Guard.MaxConnectionsPerIP = 2
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
	c.UI.Clipboard.ClearAfterSec = intPtr(30)
	c.History.InitialLines = intPtr(DefaultHistoryInitialLines)
	c.History.BatchLines = intPtr(DefaultHistoryBatchLines)
	c.History.RecoverRetries = intPtr(DefaultHistoryRecoverRetries)
	c.History.RecoverRetryDelay = strPtr("2s")
	c.History.LiveRecoverCap = intPtr(DefaultHistoryLiveRecoverCap)
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
	c.UI.Web.CharAspect = 0.6
	c.UI.Web.TargetCols.Coarse = 42
	c.UI.Web.TargetCols.Desktop = 105
	c.UI.Web.TargetCols.Narrow = 90
	c.UI.Web.Scrollback = 10000
	c.UI.Web.ResizeDebounce = "150ms"
	c.UI.Collapse.Enabled = boolPtr(true)
	c.UI.Collapse.Rows = 10
	c.UI.Collapse.PreviewRows = 5
	c.UI.VersionCheck.Enabled = boolPtr(true)
	c.UI.VersionCheck.Mode = "warn"
	c.UI.VersionCheck.Expect = ""
	c.UI.Notify = map[string]bool{}
	c.UI.PowMinTier = intPtr(1)
	c.Commands = map[string][]string{
		"quit":         {"/quit", "/q"},
		"clear":        {"/clear", "/c"},
		"clearHistory": {"/clearhistory", "/ch"},
		"showJoin":     {"/showjoin", "/sj"},
		"autoVerify":   {"/autoverify", "/av"},
		"info":         {"/info"},
		"copy":         {"/copy"},
		"tab":          {"/tab", "/t"},
		"meta":         {"/meta", "/m"},
		"find":         {"/find", "/f"},
		"reply":        {"/reply"},
		"notify":       {"/notify", "/nt"},
		"whoami":       {"/whoami", "/w"},
		"status":       {"/status"},
		"help":         {"/help", "/h"},
	}
	c.Tabs.ChatMaxBytes = 2097152
	c.Tabs.SystemMaxLines = 2000
	c.Tabs.SystemMaxBytes = 409600
	c.Pow.MaxTier = 3
	c.Pow.MaxCostMs = 120000
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

// boolPtr boxes a bool default so JSON round-trips tell "absent" from "false".
func boolPtr(v bool) *bool    { return &v }
func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

// ShowMeta reports whether meta lines render. Nil means shown.
func (c *ClientConfig) ShowMeta() bool {
	if c == nil || c.UI.Meta.Show == nil {
		return true
	}
	return *c.UI.Meta.Show
}

// DefaultAutoVerify reports the startup trip auto-verify value. Nil means enabled.
func (c *ClientConfig) DefaultAutoVerify() bool {
	if c == nil || c.Defaults.AutoVerify == nil {
		return true
	}
	return *c.Defaults.AutoVerify
}

// VersionCheckEnabled reports whether the pre-dial version check runs.
func (c *ClientConfig) VersionCheckEnabled() bool {
	if c == nil || c.UI.VersionCheck.Enabled == nil {
		return true
	}
	return *c.UI.VersionCheck.Enabled
}

// VersionCheckMode returns disabled, warn or enforce. Unknown values
// fall back to warn (noisy, never silent).
func (c *ClientConfig) VersionCheckMode() string {
	m := ""
	if c != nil {
		m = c.UI.VersionCheck.Mode
	}
	switch m {
	case "disabled", "warn", "enforce":
		return m
	default:
		return "warn"
	}
}

// VersionCheckExpect returns the pinned fork version, or "" to compare
// against this client's own stamp.
func (c *ClientConfig) VersionCheckExpect() string {
	if c == nil {
		return ""
	}
	return c.UI.VersionCheck.Expect
}

// NotifyGates holds muted tag paths. It decodes leniently: a non-boolean
// value is dropped, never fatal, because a strict failure discards the
// whole file and with it the username, server and every other setting.
// Pre-tag kind names ("pow", "history", "join", "date") map to the tags
// they used to drive, so an old config keeps meaning what its author set.
type NotifyGates map[string]bool

// legacyNotifyTags maps pre-tag kind names to current tag paths. "join"
// covered join and leave through one switch, so it maps to both tags:
// mapping one would un-mute half of what the author asked for. "system"
// is absent on purpose: it meant "the catch-all kind" before and means
// "everything" now, and widening a mute is not a safe guess.
var legacyNotifyTags = map[string][]string{
	"pow":     {"system.pow"},
	"history": {"system.history"},
	"join":    {"system.join", "system.leave"},
	"leave":   {"system.leave"},
	"date":    {"system.date"},
}

// UnmarshalJSON reads one gate per entry, skipping non-boolean values.
func (g *NotifyGates) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	gates := NotifyGates{}
	set := func(key string, value json.RawMessage) {
		var on bool
		if err := json.Unmarshal(value, &on); err != nil {
			return
		}
		if tags, legacy := legacyNotifyTags[key]; legacy {
			// Map order is random, so an explicit tag always wins over a
			// legacy name mapping to the same tag.
			for _, tag := range tags {
				if _, taken := gates[tag]; !taken {
					gates[tag] = on
				}
			}
			return
		}
		gates[key] = on
	}
	for key, value := range raw {
		if _, legacy := legacyNotifyTags[key]; !legacy {
			set(key, value)
		}
	}
	for key, value := range raw {
		if _, legacy := legacyNotifyTags[key]; legacy {
			set(key, value)
		}
	}
	*g = gates
	return nil
}

// NotifyTag reports whether notices with tag print live. An absent key
// means shown; a muted node covers its subtree.
func (c *ClientConfig) NotifyTag(tag string) bool {
	if c == nil {
		return true
	}
	enabled, ok := c.UI.Notify[tag]
	return !ok || enabled
}

// NotifyPowMinTier returns the lowest PoW tier with a "solving" notice
// (default 1).
func (c *ClientConfig) NotifyPowMinTier() int {
	if c == nil || c.UI.PowMinTier == nil {
		return 1
	}
	return *c.UI.PowMinTier
}

// CollapseEnabled reports whether long blocks fold.
func (c *ClientConfig) CollapseEnabled() bool {
	if c == nil || c.UI.Collapse.Enabled == nil {
		return true
	}
	return *c.UI.Collapse.Enabled
}

// CollapseRows returns the fold threshold in screen rows (floor 3,
// default 10).
func (c *ClientConfig) CollapseRows() int {
	if c == nil || c.UI.Collapse.Rows < 3 {
		return 10
	}
	return c.UI.Collapse.Rows
}

// CollapsePreviewRows returns surviving head rows, clamped below Rows.
func (c *ClientConfig) CollapsePreviewRows() int {
	rows := c.CollapseRows()
	if c == nil || c.UI.Collapse.PreviewRows <= 0 {
		return 5
	}
	if c.UI.Collapse.PreviewRows >= rows {
		return rows - 1
	}
	return c.UI.Collapse.PreviewRows
}

// MentionEnabled reports whether @#height mentions highlight.
func (c *ClientConfig) MentionEnabled() bool {
	if c == nil || c.UI.Mention.Enabled == nil {
		return true
	}
	return *c.UI.Mention.Enabled
}

// MentionColor returns the clamped highlight triple. Absent ([0,0,0])
// or out-of-range channels fall back to bright cyan.
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

// SaveMessageHistory reports whether chat messages persist in the input
// history file. Default off: slash commands persist, messages never
// touch disk.
func (c *ClientConfig) SaveMessageHistory() bool {
	if c == nil || c.UI.History.SaveMessages == nil {
		return false
	}
	return *c.UI.History.SaveMessages
}

// QuoteMaxRunes bounds reply preview length (20-200, default 80) so
// quotes stay single-line on narrow screens.
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

// ClipboardClearAfterSec seconds until /copy auto-clears (default 30,
// explicit 0 disables). Nil means the default.
func (c *ClientConfig) ClipboardClearAfterSec() int {
	if c == nil || c.UI.Clipboard.ClearAfterSec == nil {
		return 30
	}
	if *c.UI.Clipboard.ClearAfterSec < 0 {
		return 30
	}
	return *c.UI.Clipboard.ClearAfterSec
}

// HistoryInitialLines lines of recent history covered by the initial load
// (default 500; absent or non-positive means the default).
func (c *ClientConfig) HistoryInitialLines() int {
	if c == nil || c.History.InitialLines == nil || *c.History.InitialLines <= 0 {
		return DefaultHistoryInitialLines
	}
	return *c.History.InitialLines
}

// HistoryBatchLines lines requested per paging round (default 100;
// absent or non-positive means the default).
func (c *ClientConfig) HistoryBatchLines() int {
	if c == nil || c.History.BatchLines == nil || *c.History.BatchLines <= 0 {
		return DefaultHistoryBatchLines
	}
	return *c.History.BatchLines
}

// HistoryRecoverRetries max refill retry rounds (default 2; absent or
// negative means the default, explicit 0 disables retries).
func (c *ClientConfig) HistoryRecoverRetries() int {
	if c == nil || c.History.RecoverRetries == nil || *c.History.RecoverRetries < 0 {
		return DefaultHistoryRecoverRetries
	}
	return *c.History.RecoverRetries
}

// HistoryRecoverRetryDelay wait between refill retries (default 2s;
// absent or unparsable means the default).
func (c *ClientConfig) HistoryRecoverRetryDelay() time.Duration {
	if c == nil || c.History.RecoverRetryDelay == nil {
		return DefaultHistoryRecoverRetryDelay
	}
	d, err := time.ParseDuration(*c.History.RecoverRetryDelay)
	if err != nil || d <= 0 {
		return DefaultHistoryRecoverRetryDelay
	}
	return d
}

// HistoryLiveRecoverCap per-session live-gap refill budget in lines
// (default 1000; explicit 0 means unlimited; absent or negative means
// the default).
func (c *ClientConfig) HistoryLiveRecoverCap() int {
	if c == nil || c.History.LiveRecoverCap == nil || *c.History.LiveRecoverCap < 0 {
		return DefaultHistoryLiveRecoverCap
	}
	return *c.History.LiveRecoverCap
}

// Load reads a client config file. A missing file yields in-memory
// defaults; nothing is ever written back. Mutable state lives in
// separate cache files.
func Load(path string) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultClientConfig(), nil
		}
		return nil, err
	}
	if identity.IsEncryptedData(data) {
		return nil, ErrEncrypted
	}
	return parse(data)
}

// LoadEncrypted opens a passphrase-sealed config file. A wrong passphrase
// and a corrupt file both fail closed.
func LoadEncrypted(path string, passphrase []byte) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	plain, err := identity.DecryptData(data, passphrase)
	if err != nil {
		return nil, err
	}
	defer identity.ZeroBytes(plain)
	return parse(plain)
}

// ReplaceFile atomically swaps the file at path with data. Explicit
// replacements are the only write path; readers never mutate in place.
// Owner-only permissions, like every other secret-adjacent file.
func ReplaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := filepath.Join(dir, ".tmp-config.jsonc")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parse(data []byte) (*ClientConfig, error) {
	var c ClientConfig
	stripped, err := StripComments(data)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(stripped, &c); err != nil {
		return nil, err
	}
	// Backfill tab caps for files written before the tabs section existed.
	def := DefaultClientConfig()
	// A partial config must never zero out the send guards (a zero
	// MaxMessageLength would reject every message).
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
	// Guard carries the template duration strings; explicit values win
	// over the Limits defaults, unparsable or empty keeps the default.
	if d, err := time.ParseDuration(c.Guard.MessageCooldown); err == nil && d > 0 {
		c.Limits.MessageCooldown = d
	}
	if d, err := time.ParseDuration(c.Guard.ConnectionCooldown); err == nil && d > 0 {
		c.Limits.ConnectionCooldown = d
	}
	if d, err := time.ParseDuration(c.Guard.IdleTimeout); err == nil && d > 0 {
		c.Limits.IdleChatTimeout = d
	}
	if c.Guard.MaxConnectionsPerIP > 0 {
		c.Limits.MaxConnectionsPerIP = c.Guard.MaxConnectionsPerIP
	}
	// Absent meta section means shown.
	if c.UI.Meta.Show == nil {
		c.UI.Meta.Show = def.UI.Meta.Show
	}
	// Absent auto-verify means enabled.
	if c.Defaults.AutoVerify == nil {
		c.Defaults.AutoVerify = def.Defaults.AutoVerify
	}
	// Absent recovery fields mean their defaults.
	if c.History.InitialLines == nil {
		c.History.InitialLines = def.History.InitialLines
	}
	if c.History.BatchLines == nil {
		c.History.BatchLines = def.History.BatchLines
	}
	if c.History.RecoverRetries == nil {
		c.History.RecoverRetries = def.History.RecoverRetries
	}
	if c.History.RecoverRetryDelay == nil {
		c.History.RecoverRetryDelay = def.History.RecoverRetryDelay
	}
	if c.History.LiveRecoverCap == nil {
		c.History.LiveRecoverCap = def.History.LiveRecoverCap
	}
	// Absent version-check knobs mean enabled warn.
	if c.UI.VersionCheck.Enabled == nil {
		c.UI.VersionCheck.Enabled = def.UI.VersionCheck.Enabled
	}
	if c.UI.VersionCheck.Mode == "" {
		c.UI.VersionCheck.Mode = def.UI.VersionCheck.Mode
	}
	// Notify needs no backfill: an absent key already means shown.
	if c.UI.Notify == nil {
		c.UI.Notify = map[string]bool{}
	}
	if c.UI.PowMinTier == nil {
		c.UI.PowMinTier = def.UI.PowMinTier
	}
	// Absent collapse knobs mean enabled 10/5.
	if c.UI.Collapse.Enabled == nil {
		c.UI.Collapse.Enabled = def.UI.Collapse.Enabled
	}
	if c.UI.Collapse.Rows <= 0 {
		c.UI.Collapse.Rows = def.UI.Collapse.Rows
	}
	if c.UI.Collapse.PreviewRows <= 0 {
		c.UI.Collapse.PreviewRows = def.UI.Collapse.PreviewRows
	}
	// Same backfill for mention/reply knobs.
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
	// Absent autoclear means default 30; explicit 0 stays 0 to disable.
	if c.UI.Clipboard.ClearAfterSec == nil {
		c.UI.Clipboard.ClearAfterSec = def.UI.Clipboard.ClearAfterSec
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
	// [0,0,0] means "use default".
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
	// Absent PoW bounds must not zero the decline guards (MaxTier 0
	// would decline everything).
	if c.Pow.MaxTier <= 0 {
		c.Pow.MaxTier = def.Pow.MaxTier
	}
	if c.Pow.MaxCostMs <= 0 {
		c.Pow.MaxCostMs = def.Pow.MaxCostMs
	}
	return &c, nil
}
