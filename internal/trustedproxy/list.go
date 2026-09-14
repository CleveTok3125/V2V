package trustedproxy

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Limits bound directory scans and log lines against filesystem abuse:
// an operator (or anyone with config-dir write access) must not be
// able to stretch boot logs or memory by dumping files in.
const (
	maxTrustDirEntries = 128
	maxExtraWarnNames  = 10
	maxWarnNameLen     = 64
)

// Set is one parsed "<name>.txt" trust file.
type Set struct {
	Path string
	Nets []*net.IPNet
	// Missing is true when the file did not exist. Only tolerated
	// for optional providers (direct); header providers fail load.
	Missing bool
}

// LoadTrustDir loads "<name>.txt" for every active provider from dir
// and returns the parsed sets keyed by provider name, plus at most
// one aggregated warning about stray files. Any hard error (missing
// directory, missing required file, malformed entry) fails the whole
// load: trust must never silently degrade at boot.
func LoadTrustDir(dir string, active []string, required map[string]bool) (map[string]*Set, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", fmt.Errorf("trusted proxy dir %q unreadable: %w", dir, err)
	}
	if len(entries) > maxTrustDirEntries {
		entries = entries[:maxTrustDirEntries]
	}
	inActive := map[string]bool{}
	for _, name := range active {
		inActive[name] = true
	}
	sets := make(map[string]*Set, len(active))
	for _, name := range active {
		path := filepath.Join(dir, name+".txt")
		set, err := loadTrustFile(path)
		if err != nil {
			if os.IsNotExist(err) && !required[name] {
				sets[name] = &Set{Path: path, Missing: true}
				continue
			}
			return nil, "", err
		}
		sets[name] = set
	}
	var extra []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".txt") {
			continue
		}
		if !inActive[strings.TrimSuffix(name, ".txt")] {
			extra = append(extra, name)
		}
	}
	return sets, extraWarning(extra), nil
}

// extraWarning folds stray files into a single bounded line: one file
// per line would let a directory dump flood the logs.
func extraWarning(extra []string) string {
	if len(extra) == 0 {
		return ""
	}
	shown := extra
	more := ""
	if len(shown) > maxExtraWarnNames {
		shown = shown[:maxExtraWarnNames]
		more = fmt.Sprintf(", ... and %d more", len(extra)-maxExtraWarnNames)
	}
	for i, name := range shown {
		shown[i] = Clip(name, maxWarnNameLen)
	}
	return fmt.Sprintf("extra trust files ignored: %d (%s%s)", len(extra), strings.Join(shown, ", "), more)
}

// loadTrustFile parses one trust file: one IP or CIDR per line,
// "#" comments, blank lines skipped. A bare IP becomes a /32 (v4) or
// /128 (v6) net. The first malformed line fails the file with its
// line number.
func loadTrustFile(path string) (*Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := &Set{Path: path}
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		var ipNet *net.IPNet
		if _, parsed, err := net.ParseCIDR(trimmed); err == nil {
			ipNet = parsed
		} else if ip := net.ParseIP(trimmed); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			ipNet = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
		} else {
			return nil, fmt.Errorf("%s:%d: invalid IP or CIDR %q", path, i+1, Clip(trimmed, maxWarnNameLen))
		}
		set.Nets = append(set.Nets, ipNet)
	}
	return set, nil
}
