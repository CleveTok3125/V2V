package main

// Pre-dial server version check. Policy lives entirely client-side and
// is configurable (ui.versionCheck): exact string match against our own
// stamp or a pinned fork version, no semver parsing so forks with any
// scheme work. WASM skips (paired with its serving server).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

type versionVerdict int

const (
	versionMatch versionVerdict = iota
	versionMismatch
	versionUnknown
)

// versionCore strips a stamp down to the commit it was built from so
// different schemes for the same commit compare equal: release tags
// ("v0.9.0-18-g4be8087" -> "4be8087"), dev stamps ("dev-4be8087" ->
// "4be8087"), anything else compares verbatim. The second return
// reports a "-dirty" working tree, whose content genuinely differs.
func versionCore(v string) (core string, dirty bool) {
	dirty = strings.HasSuffix(v, "-dirty")
	core = strings.TrimSuffix(v, "-dirty")
	if i := strings.LastIndex(core, "-g"); i >= 0 {
		core = core[i+2:]
	} else {
		core = strings.TrimPrefix(core, "dev-")
	}
	return core, dirty
}

// decideVersionCheck maps mode + versions to a verdict. Pure: the whole
// policy matrix is unit-tested here, I/O stays in checkServerVersion.
// Same commit on both sides matches; a dirty tree on either side still
// warns (enforce aborts) because the content may genuinely differ.
func decideVersionCheck(mode, server, expected string) versionVerdict {
	if server == "" {
		return versionUnknown
	}
	if server == expected {
		return versionMatch
	}
	serverCore, serverDirty := versionCore(server)
	expectedCore, expectedDirty := versionCore(expected)
	if serverCore != "" && serverCore == expectedCore {
		if serverDirty || expectedDirty {
			return versionMismatch
		}
		return versionMatch
	}
	return versionMismatch
}

// httpBaseFromWS derives an http(s) base URL from a normalized ws(s) URL.
func httpBaseFromWS(wsURL string) (string, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("not a websocket URL: %s", wsURL)
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// fetchServerVersion GETs /api/version. Empty string means unknown
// (old server, fork without the endpoint, or network error).
func fetchServerVersion(httpBase string) string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(httpBase + "/api/version")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	if err != nil {
		return ""
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	return v.Version
}

// checkServerVersion runs the pre-dial check. False means enforce mode
// refused to continue; anything else proceeds.
func checkServerVersion(wsURL string) bool {
	if runtime.GOOS == "js" {
		return true // paired with the serving server
	}
	if ClientCfg == nil || !ClientCfg.VersionCheckEnabled() {
		return true
	}
	mode := ClientCfg.VersionCheckMode()
	if mode == "disabled" {
		return true
	}
	expected := ClientCfg.VersionCheckExpect()
	if expected == "" {
		expected = Version
	}
	base, err := httpBaseFromWS(wsURL)
	server := ""
	if err == nil {
		server = fetchServerVersion(base)
	}
	switch decideVersionCheck(mode, server, expected) {
	case versionMatch:
		return true
	case versionUnknown:
		fmt.Printf("⚠️ Không xác định được phiên bản server (muốn %q) — tiếp tục.\n", expected)
		if mode == "enforce" {
			fmt.Println("❌ Chế độ enforce: dừng vì không xác định được phiên bản.")
			return false
		}
		return true
	default:
		fmt.Printf("⚠️ Phiên bản server %q khác %q — tiếp tục.\n", server, expected)
		if mode == "enforce" {
			fmt.Printf("❌ Chế độ enforce: dừng vì phiên bản khác %q.\n", expected)
			return false
		}
		return true
	}
}
