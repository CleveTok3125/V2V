package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/CleveTok3125/V2V/internal/tui"
)

// ConfigTakeCmd implements `v2vctl config take`: list drifted keys
// (local value differs from template), pick them, and apply the take.
// Drift detection reuses mergeAll; applying reuses the sync write path,
// so preview, force-gating and backups behave identically.
type ConfigTakeCmd struct {
	ConfigCommon `embed:""`
	List         bool `help:"Chỉ in id:key mỗi dòng, không hỏi, không ghi"`
	Yes          bool `help:"Áp dụng mọi mục drift mà không hỏi (scriptable)"`
	Force        bool `help:"Chuyển tiếp cho sync khi entry lossy (vd jsonc)"`
}

// takeCandidate is one resettable drift: a key whose local value
// differs from template, or a trust file with local-only entries
// (Key "" = whole file).
type takeCandidate struct {
	ID          string
	Key         string
	Local       string
	Template    string
	Preselected bool
}

// Spec renders the --prefer-template spec: "id" or "id:key".
func (c takeCandidate) Spec() string {
	if c.Key == "" {
		return c.ID
	}
	return c.ID + ":" + c.Key
}

// Label renders one picker row: what resets and both values.
func (c takeCandidate) Label() string {
	if c.Key == "" {
		return fmt.Sprintf("%s (whole file) | local-only entries will be dropped", c.ID)
	}
	return fmt.Sprintf("%s:%s | local=%s | template=%s", c.ID, c.Key, c.Local, c.Template)
}

// takeSpecList renders one spec per line for piping into sync.
func takeSpecList(cands []takeCandidate) string {
	var b strings.Builder
	for _, c := range cands {
		b.WriteString(c.Spec())
		b.WriteByte('\n')
	}
	return b.String()
}

// envLineValue finds the display value for key in an .env document:
// the active line when present, else the commented default.
func envLineValue(data []byte, key string) (string, bool) {
	var commented string
	var hasCommented bool
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		rest := t
		isComment := false
		if strings.HasPrefix(rest, "#") {
			isComment = true
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "#"))
		}
		if strings.HasPrefix(rest, "export ") {
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "export "))
		}
		eq := strings.Index(rest, "=")
		if eq < 0 {
			continue
		}
		if strings.TrimSpace(rest[:eq]) != key {
			continue
		}
		v := strings.TrimSpace(rest[eq+1:])
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		if !isComment {
			return v, true
		}
		if !hasCommented {
			commented, hasCommented = v, true
		}
	}
	return commented, hasCommented
}

// jsonTopValue renders one top-level JSON value compactly for display.
func jsonTopValue(doc []byte, key string) (string, bool) {
	var obj map[string]any
	if err := json.Unmarshal(doc, &obj); err != nil {
		return "", false
	}
	v, ok := obj[key]
	if !ok {
		return "", false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	s := string(b)
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s, true
}

// takeCandidates builds the drift list in manifest order: env/json
// keys whose local value differs from template, plus one entry per
// trust file holding local-only entries. Everything drifted starts
// preselected; the picker (or --yes/--list) narrows it down.
func takeCandidates(ctx *configCtx, merges []fileMerge) []takeCandidate {
	byID := map[string][]fileMerge{}
	for _, fm := range merges {
		byID[fm.ID] = append(byID[fm.ID], fm)
	}
	var out []takeCandidate
	for _, mf := range ctx.files {
		switch mf.Format {
		case "env", "json", "jsonc":
			tplBytes, err := os.ReadFile(filepath.Join(ctx.tplRoot, mf.Path))
			if err != nil {
				continue
			}
			if mf.Format == "jsonc" {
				tplBytes = stripJSONComments(tplBytes)
			}
			for _, fm := range byID[mf.ID] {
				for _, k := range fm.Overridden {
					var local, tpl string
					if mf.Format == "env" {
						local, _ = envLineValue(fm.Current, k)
						tpl, _ = envLineValue(tplBytes, k)
					} else {
						local, _ = jsonTopValue(fm.Current, k)
						tpl, _ = jsonTopValue(tplBytes, k)
					}
					out = append(out, takeCandidate{ID: mf.ID, Key: k, Local: local, Template: tpl, Preselected: true})
				}
			}
		case "trust-dir":
			seen := false
			for _, fm := range byID[mf.ID] {
				if len(fm.Orphaned) > 0 {
					seen = true
				}
			}
			if seen {
				out = append(out, takeCandidate{ID: mf.ID, Preselected: true})
			}
		}
	}
	return out
}

// pickTakeSpecs runs the huh picker (preselected drift) plus the
// bottom confirmation. Nil slice means cancelled.
func pickTakeSpecs(cands []takeCandidate) ([]string, error) {
	opts := make([]huh.Option[string], 0, len(cands))
	for _, c := range cands {
		opts = append(opts, huh.NewOption(c.Label(), c.Spec()).Selected(c.Preselected))
	}
	var picked []string
	var confirm bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().Title("Chọn mục reset về template (space chọn/bỏ)").Options(opts...).Value(&picked),
		),
		huh.NewGroup(
			huh.NewConfirm().Title("Áp dụng các mục đã chọn?").Affirmative("Có").Negative("Không").Value(&confirm),
		),
	)
	if err := form.Run(); err != nil {
		return nil, err
	}
	if !confirm || len(picked) == 0 {
		return nil, nil
	}
	return picked, nil
}

func (t *ConfigTakeCmd) Run() error {
	ctx, err := resolveConfigCtx(t.ConfigCommon)
	if err != nil {
		return err
	}
	merges, err := mergeAll(ctx)
	if err != nil {
		return err
	}
	cands := takeCandidates(ctx, merges)
	if t.List {
		fmt.Print(takeSpecList(cands))
		return nil
	}
	if len(cands) == 0 {
		fmt.Println("no drift: nothing to take")
		return nil
	}
	var specs []string
	if t.Yes {
		for _, c := range cands {
			specs = append(specs, c.Spec())
		}
	} else {
		if !tui.HasControllingTTY() {
			return fmt.Errorf("no drift selection without a terminal: rerun with --list to print specs or --yes to take all %d", len(cands))
		}
		var err error
		specs, err = pickTakeSpecs(cands)
		if err != nil {
			return err
		}
		if len(specs) == 0 {
			fmt.Println("cancelled: nothing selected")
			return nil
		}
	}
	inner := &ConfigSyncCmd{Force: t.Force, Yes: true, Quiet: true}
	inner.ConfigCommon = t.ConfigCommon
	inner.PreferTemplate = specs
	if err := inner.Run(); err != nil {
		return err
	}
	fmt.Printf("take applied: %s\n", strings.Join(specs, ", "))
	return nil
}
