package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CleveTok3125/V2V/internal/configdir"
	"github.com/CleveTok3125/V2V/internal/configmerge"
	"github.com/CleveTok3125/V2V/internal/identity"
	"github.com/pmezard/go-difflib/difflib"
)

const (
	manifestName = "v2v-template.json"
	manifestType = "v2v-template"
)

type ConfigCmd struct {
	Sync     ConfigSyncCmd     `cmd:"" help:"Đồng bộ template vào config, giữ giá trị operator đã đặt"`
	Diff     ConfigDiffCmd     `cmd:"" help:"Xem trước kết quả đồng bộ dạng unified diff"`
	Manifest ConfigManifestCmd `cmd:"" help:"Sinh lại keys trong v2v-template.json từ template"`
	Check    ConfigCheckCmd    `cmd:"" help:"Kiểm tra manifest khớp với template"`
}

// ConfigCommon holds flags shared by sync/diff/check.
type ConfigCommon struct {
	Dir       string `help:"Thư mục chứa v2v-template.json" default:"."`
	To        string `help:"Thư mục gốc config (mặc định = --dir)"`
	Only      string `help:"Tập con id trong manifest (mặc định env,roles,trust)"`
	ClientDir string `help:"Thư mục config client cho entry target=client"`
	NoPager   bool   `help:"In thẳng, không qua pager"`
}

type ConfigSyncCmd struct {
	ConfigCommon `embed:""`
	DryRun       bool `help:"Xem trước, không ghi file"`
	Force        bool `help:"Ép ghi cả entry cần can thiệp tay (vd client jsonc mất comment)"`
}

type ConfigDiffCmd struct {
	ConfigCommon `embed:""`
	Format       string `help:"Định dạng: text|json" default:"text"`
}

type ConfigCheckCmd struct {
	ConfigCommon `embed:""`
	Format       string `help:"Định dạng: text|json" default:"text"`
}

type ConfigManifestCmd struct {
	Dir   string `help:"Thư mục chứa v2v-template.json" default:"."`
	Write bool   `help:"Ghi manifest thay vì in ra"`
}

// manifestFile is one managed template file: template-relative Source, the
// config-relative Dest, the parser Format, and (for env/json/jsonc) the
// expected key order.
type manifestFile struct {
	ID     string   `json:"id"`
	Path   string   `json:"path"`
	Format string   `json:"format"`
	Dest   string   `json:"dest"`
	Target string   `json:"target,omitempty"`
	Keys   []string `json:"keys,omitempty"`
}

// manifestAddPolicy marks keys that must not inherit the template value when
// newly added: env keys render commented ("#KEY=value"), json/jsonc keys are
// skipped entirely.
type manifestAddPolicy struct {
	Env   map[string][]string `json:"env,omitempty"`
	Roles map[string][]string `json:"roles,omitempty"`
	JSONC map[string][]string `json:"jsonc,omitempty"`
}

type templateManifest struct {
	Version     int               `json:"version"`
	Type        string            `json:"type"`
	TemplateDir string            `json:"templateDir"`
	Files       []manifestFile    `json:"files"`
	AddPolicy   manifestAddPolicy `json:"add_policy"`
}

func loadManifest(dir string) (*templateManifest, string, error) {
	path := filepath.Join(dir, manifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("missing manifest %s: %w", path, err)
	}
	var m templateManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, path, fmt.Errorf("manifest %s invalid: %w", path, err)
	}
	if m.Type != manifestType {
		return nil, path, fmt.Errorf("manifest %s: type %q, want %q", path, m.Type, manifestType)
	}
	if strings.TrimSpace(m.TemplateDir) == "" {
		return nil, path, fmt.Errorf("manifest %s: templateDir is empty", path)
	}
	if len(m.Files) == 0 {
		return nil, path, fmt.Errorf("manifest %s: no files", path)
	}
	return &m, path, nil
}

func defaultManifest() *templateManifest {
	return &templateManifest{
		Version:     1,
		Type:        manifestType,
		TemplateDir: "template",
		Files: []manifestFile{
			{ID: "env", Path: ".env", Format: "env", Dest: ".env"},
			{ID: "roles", Path: "server/config/roles.json", Format: "json", Dest: "config/roles.json"},
			{ID: "trust", Path: "server/config/trustedproxy", Format: "trust-dir", Dest: "config/trustedproxy"},
			{ID: "client", Path: "client/config.jsonc", Format: "jsonc", Target: "client", Dest: "config.jsonc"},
		},
	}
}

func manifestIDs(m *templateManifest) []string {
	var ids []string
	for _, f := range m.Files {
		ids = append(ids, f.ID)
	}
	return ids
}

func selectFiles(m *templateManifest, only string) ([]manifestFile, error) {
	want := map[string]bool{}
	n := 0
	for _, part := range strings.Split(only, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if !want[name] {
			want[name] = true
			n++
		}
	}
	if n == 0 {
		var out []manifestFile
		for _, f := range m.Files {
			switch f.ID {
			case "env", "roles", "trust":
				out = append(out, f)
			}
		}
		if len(out) == 0 {
			return m.Files, nil
		}
		return out, nil
	}
	known := map[string]bool{}
	for _, f := range m.Files {
		known[f.ID] = true
	}
	for id := range want {
		if !known[id] {
			return nil, fmt.Errorf("unknown --only %q: manifest ids are %s", id, strings.Join(manifestIDs(m), ","))
		}
	}
	var out []manifestFile
	for _, f := range m.Files {
		if want[f.ID] {
			out = append(out, f)
		}
	}
	return out, nil
}

type configCtx struct {
	m           *templateManifest
	manifestDir string
	tplRoot     string
	cfgRoot     string
	clientDir   string
	files       []manifestFile
}

func resolveConfigCtx(common ConfigCommon) (*configCtx, error) {
	dir, err := filepath.Abs(common.Dir)
	if err != nil {
		return nil, err
	}
	m, _, err := loadManifest(dir)
	if err != nil {
		return nil, err
	}
	tplRoot := filepath.Clean(filepath.Join(dir, m.TemplateDir))
	if !dirExists(tplRoot) {
		return nil, fmt.Errorf("manifest templateDir %q not found at %s", m.TemplateDir, tplRoot)
	}
	toRaw := strings.TrimSpace(common.To)
	if toRaw == "" {
		toRaw = common.Dir
	}
	cfgRoot, err := filepath.Abs(toRaw)
	if err != nil {
		return nil, err
	}
	if pathWithin(cfgRoot, tplRoot) {
		return nil, fmt.Errorf("refusing: config root %s is inside the template root %s", cfgRoot, tplRoot)
	}
	clientDir := common.ClientDir
	if clientDir == "" {
		clientDir = configdir.DefaultConfigDir()
	}
	files, err := selectFiles(m, common.Only)
	if err != nil {
		return nil, err
	}
	return &configCtx{
		m:           m,
		manifestDir: dir,
		tplRoot:     tplRoot,
		cfgRoot:     cfgRoot,
		clientDir:   clientDir,
		files:       files,
	}, nil
}

func (c *configCtx) dstFor(mf manifestFile) string {
	if mf.Target == "client" {
		return filepath.Join(c.clientDir, mf.Dest)
	}
	return filepath.Join(c.cfgRoot, mf.Dest)
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// pathWithin reports whether child is parent itself or lives under it.
func pathWithin(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func policySet(m map[string][]string, mode string) map[string]bool {
	out := map[string]bool{}
	for _, k := range m[mode] {
		out[k] = true
	}
	return out
}

func containsString(list []string, key string) bool {
	for _, v := range list {
		if v == key {
			return true
		}
	}
	return false
}

func readFileOrEmpty(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []byte{}, nil
	}
	return data, err
}

// validateJSONObject rejects a present but malformed JSON document so a
// broken config is never silently treated as empty and overwritten.
func validateJSONObject(path string, data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("%s: invalid JSON (%w); refusing to overwrite", path, err)
	}
	return nil
}

// fileMerge is one merged file: Rel labels the diff, Dst is where the
// merged bytes are written, Current the present content. RequiresForce
// marks a lossy merge (manual review needed before writing).
type fileMerge struct {
	ID            string
	Rel           string
	Dst           string
	Current       []byte
	Merged        []byte
	Added         []string
	Overridden    []string
	Orphaned      []string
	Kept          []string
	EnvValues     map[string]string
	JSONObject    map[string]any
	TrustEntries  []string
	RequiresForce bool
}

// mergeAll overlays config values onto the template structure for every
// selected manifest file. It is the single source for diff, sync and check.
func mergeAll(ctx *configCtx) ([]fileMerge, error) {
	var out []fileMerge
	for _, mf := range ctx.files {
		switch mf.Format {
		case "env":
			tplData, err := os.ReadFile(filepath.Join(ctx.tplRoot, mf.Path))
			if err != nil {
				return nil, fmt.Errorf("template %s: %w", mf.Path, err)
			}
			dst := ctx.dstFor(mf)
			cur, _ := readFileOrEmpty(dst)
			res := configmerge.OverlayEnv(cur, tplData)
			comment := policySet(ctx.m.AddPolicy.Env, "comment")
			merged := configmerge.RenderEnvWithPolicy(res, tplData, comment)
			active := map[string]string{}
			for k, v := range res.Values {
				if comment[k] && containsString(res.Added, k) {
					continue
				}
				active[k] = v
			}
			out = append(out, fileMerge{
				ID: mf.ID, Rel: mf.Path, Dst: dst, Current: cur, Merged: merged,
				Added: res.Added, Overridden: res.ChangedUpstream,
				Orphaned: res.Orphaned, Kept: res.Kept, EnvValues: active,
			})
		case "json", "jsonc":
			tplData, err := os.ReadFile(filepath.Join(ctx.tplRoot, mf.Path))
			if err != nil {
				return nil, fmt.Errorf("template %s: %w", mf.Path, err)
			}
			dst := ctx.dstFor(mf)
			curRaw, _ := readFileOrEmpty(dst)
			if mf.Format == "jsonc" {
				tplData = stripJSONComments(tplData)
				curRaw = stripJSONComments(curRaw)
			}
			if err := validateJSONObject(filepath.Join(ctx.tplRoot, mf.Path), tplData); err != nil {
				return nil, err
			}
			if err := validateJSONObject(dst, curRaw); err != nil {
				return nil, err
			}
			if len(bytes.TrimSpace(curRaw)) == 0 {
				curRaw = []byte("{}")
			}
			skip := policySet(addPolicyFor(ctx.m, mf), "skip")
			res := configmerge.OverlayJSONWithSkip(curRaw, tplData, skip)
			rendered, err := configmerge.RenderJSON(res, tplData)
			if err != nil {
				return nil, err
			}
			out = append(out, fileMerge{
				ID: mf.ID, Rel: mf.Path, Dst: dst, Current: curRaw, Merged: rendered,
				Added: res.Added, Overridden: res.Overridden,
				Orphaned: res.Orphaned, Kept: res.Kept, JSONObject: res.Object,
				// JSONC round-trips through encoding/json, so comments and
				// original formatting are lost: require an explicit --force.
				RequiresForce: mf.Format == "jsonc",
			})
		case "trust-dir":
			srcDir := filepath.Join(ctx.tplRoot, mf.Path)
			entries, err := os.ReadDir(srcDir)
			if err != nil {
				return nil, fmt.Errorf("template trust dir %s: %w", mf.Path, err)
			}
			var names []string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			dstDir := ctx.dstFor(mf)
			for _, name := range names {
				tplData, err := os.ReadFile(filepath.Join(srcDir, name))
				if err != nil {
					return nil, err
				}
				dst := filepath.Join(dstDir, name)
				cur, _ := readFileOrEmpty(dst)
				base := tplData
				if len(base) == 0 {
					base = cur
				}
				res := configmerge.OverlayTrust(cur, tplData)
				out = append(out, fileMerge{
					ID: mf.ID, Rel: filepath.ToSlash(filepath.Join(mf.Path, name)),
					Dst: dst, Current: cur, Merged: configmerge.RenderTrust(res, base),
					Added: res.Added, Orphaned: res.Orphaned, Kept: res.Kept,
					TrustEntries: res.Entries,
				})
			}
		default:
			return nil, fmt.Errorf("manifest %s: unknown format %q", mf.ID, mf.Format)
		}
	}
	return out, nil
}

func addPolicyFor(m *templateManifest, mf manifestFile) map[string][]string {
	switch mf.ID {
	case "env":
		return m.AddPolicy.Env
	case "roles":
		return m.AddPolicy.Roles
	case "client":
		return m.AddPolicy.JSONC
	}
	return nil
}

func unifiedFileDiff(fm fileMerge) string {
	diff := difflib.UnifiedDiff{
		A:        normLines(fm.Current),
		B:        normLines(fm.Merged),
		FromFile: "config/" + fm.Rel,
		ToFile:   "merged/" + fm.Rel,
		Context:  3,
	}
	text, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return ""
	}
	return text
}

func normLines(data []byte) []string {
	text := string(data)
	if text == "" {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return difflib.SplitLines(text)
}

func unifiedDiffsText(merges []fileMerge) (string, bool) {
	var out strings.Builder
	changed := false
	for _, fm := range merges {
		text := unifiedFileDiff(fm)
		if text == "" {
			continue
		}
		changed = true
		out.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			out.WriteString("\n")
		}
	}
	return out.String(), changed
}

func reportFromMerges(merges []fileMerge) map[string]any {
	report := map[string]any{}
	for _, fm := range merges {
		report[fm.Rel] = map[string]any{
			"added": fm.Added, "overridden": fm.Overridden,
			"orphaned": fm.Orphaned, "kept": fm.Kept,
		}
	}
	return report
}

func verifyMergeOut(fm fileMerge) error {
	switch {
	case fm.EnvValues != nil:
		return verifyEnvOut(fm.Merged, fm.EnvValues)
	case fm.JSONObject != nil:
		return verifyJSONOut(fm.Merged, fm.JSONObject)
	default:
		return verifyTrustOut(fm.Merged, fm.TrustEntries)
	}
}

func verifyEnvOut(out []byte, want map[string]string) error {
	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		key, val, ok := configmerge.ParseEnvLine(line)
		if !ok {
			continue
		}
		got[key] = val
	}
	for key, val := range want {
		gotVal, ok := got[key]
		if !ok {
			return fmt.Errorf("rendered .env missing key %s", key)
		}
		if gotVal != val {
			return fmt.Errorf("rendered .env key %s mismatch: wrote %q want %q", key, gotVal, val)
		}
	}
	return nil
}

// verifyJSONOut ensures rendered JSON parses and carries merged keys.
func verifyJSONOut(out []byte, want map[string]any) error {
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		return fmt.Errorf("rendered JSON invalid: %w", err)
	}
	for key := range want {
		if _, ok := root[key]; !ok {
			return fmt.Errorf("rendered JSON missing key %s", key)
		}
	}
	return nil
}

// verifyTrustOut ensures every merged entry survived the render.
func verifyTrustOut(out []byte, want []string) error {
	got := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		got[strings.ToLower(trimmed)] = true
	}
	for _, e := range want {
		if !got[e] {
			return fmt.Errorf("rendered trust file missing entry %s", e)
		}
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	return identity.WriteConfigFile(path, data)
}

func stripJSONComments(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	var out []byte
	inString := false
	escaped := false
	i := 0
	for i < len(data) {
		c := data[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			i++
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			i++
			continue
		}
		if c == '/' && i+1 < len(data) && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(data) && data[i+1] == '*' {
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		out = append(out, c)
		i++
	}
	return out
}

func (s *ConfigSyncCmd) Run() error {
	ctx, err := resolveConfigCtx(s.ConfigCommon)
	if err != nil {
		return err
	}
	merges, err := mergeAll(ctx)
	if err != nil {
		return err
	}
	text, drift := unifiedDiffsText(merges)
	if !drift {
		text = "already in sync\n"
	}
	emitPaged(text, s.NoPager)
	if s.DryRun {
		fmt.Println("dry-run: no files written")
		return nil
	}
	if !drift {
		return nil
	}
	for _, fm := range merges {
		if err := verifyMergeOut(fm); err != nil {
			return err
		}
		if pathWithin(fm.Dst, ctx.tplRoot) {
			return fmt.Errorf("refusing to write %s: inside template root %s", fm.Dst, ctx.tplRoot)
		}
	}
	if !s.Force {
		var blocked []string
		for _, fm := range merges {
			if fm.RequiresForce && !bytes.Equal(fm.Current, fm.Merged) {
				blocked = append(blocked, fm.Rel)
			}
		}
		if len(blocked) > 0 {
			return fmt.Errorf("manual review required for %s (lossy: comments/format normalized); run: v2vctl config diff --dir %s --only <id>, then rerun with --force",
				strings.Join(blocked, ", "), s.Dir)
		}
	}
	written := 0
	for _, fm := range merges {
		if bytes.Equal(fm.Current, fm.Merged) {
			continue
		}
		if err := writeAtomic(fm.Dst, fm.Merged); err != nil {
			return err
		}
		written++
	}
	fmt.Printf("synced (%d files)\n", written)
	return nil
}

func (d *ConfigDiffCmd) Run() error {
	format, err := parseFormat(d.Format)
	if err != nil {
		return err
	}
	ctx, err := resolveConfigCtx(d.ConfigCommon)
	if err != nil {
		return err
	}
	merges, err := mergeAll(ctx)
	if err != nil {
		return err
	}
	if format == "json" {
		out, _ := json.MarshalIndent(reportFromMerges(merges), "", "  ")
		fmt.Println(string(out))
		return nil
	}
	text, changed := unifiedDiffsText(merges)
	if !changed {
		text = "already in sync\n"
	}
	emitPaged(text, d.NoPager)
	return nil
}

func parseFormat(raw string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	if format == "" {
		return "text", nil
	}
	if format != "text" && format != "json" {
		return "", fmt.Errorf("unknown --format %q (want text|json)", raw)
	}
	return format, nil
}

func (c *ConfigCheckCmd) Run() error {
	format, err := parseFormat(c.Format)
	if err != nil {
		return err
	}
	ctx, err := resolveConfigCtx(c.ConfigCommon)
	if err != nil {
		return err
	}
	problems := checkManifest(ctx)
	if format == "json" {
		out, _ := json.MarshalIndent(map[string]any{"ok": len(problems) == 0, "problems": problems}, "", "  ")
		fmt.Println(string(out))
	} else if len(problems) == 0 {
		fmt.Println("manifest ok")
	} else {
		for _, p := range problems {
			fmt.Println("drift: " + p)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("manifest drift (%d); run: v2vctl config manifest --dir %s --write", len(problems), c.Dir)
	}
	return nil
}

func checkManifest(ctx *configCtx) []string {
	var problems []string
	for _, mf := range ctx.files {
		src := filepath.Join(ctx.tplRoot, mf.Path)
		switch mf.Format {
		case "trust-dir":
			if !dirExists(src) {
				problems = append(problems, fmt.Sprintf("%s: missing dir %s", mf.ID, mf.Path))
			}
		case "env", "json", "jsonc":
			data, err := os.ReadFile(src)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: missing %s", mf.ID, mf.Path))
				continue
			}
			if mf.Format == "jsonc" {
				data = stripJSONComments(data)
			}
			var got []string
			if mf.Format == "env" {
				got = configmerge.EnvKeys(data)
			} else {
				got = configmerge.JSONTopKeys(data)
			}
			problems = append(problems, diffKeyOrder(mf.ID, mf.Keys, got)...)
		default:
			problems = append(problems, fmt.Sprintf("%s: unknown format %q", mf.ID, mf.Format))
		}
	}
	problems = append(problems, validateAddPolicy(ctx.m)...)
	return problems
}

func diffKeyOrder(id string, want, got []string) []string {
	var problems []string
	wantSet := map[string]bool{}
	for _, k := range want {
		wantSet[k] = true
	}
	gotSet := map[string]bool{}
	for _, k := range got {
		gotSet[k] = true
	}
	for _, k := range got {
		if !wantSet[k] {
			problems = append(problems, fmt.Sprintf("%s: key %s in template but not in manifest", id, k))
		}
	}
	for _, k := range want {
		if !gotSet[k] {
			problems = append(problems, fmt.Sprintf("%s: key %s in manifest but not in template", id, k))
		}
	}
	if len(want) == len(got) {
		for i := range want {
			if want[i] != got[i] {
				problems = append(problems, fmt.Sprintf("%s: key order differs at %d: manifest %s, template %s", id, i, want[i], got[i]))
				break
			}
		}
	}
	return problems
}

func validateAddPolicy(m *templateManifest) []string {
	var problems []string
	keysFor := func(id string) map[string]bool {
		for _, f := range m.Files {
			if f.ID == id {
				set := map[string]bool{}
				for _, k := range f.Keys {
					set[k] = true
				}
				return set
			}
		}
		return nil
	}
	check := func(id, mode string, policy map[string][]string) {
		keys := keysFor(id)
		for listedMode, list := range policy {
			if mode != "" && listedMode != mode {
				problems = append(problems, fmt.Sprintf("add_policy.%s: mode %q is invalid (want %s)", id, listedMode, mode))
				continue
			}
			for _, k := range list {
				if keys != nil && !keys[k] {
					problems = append(problems, fmt.Sprintf("add_policy.%s: key %s not in template", id, k))
				}
			}
		}
	}
	check("env", "comment", m.AddPolicy.Env)
	check("roles", "skip", m.AddPolicy.Roles)
	check("client", "skip", m.AddPolicy.JSONC)
	return problems
}

func (c *ConfigManifestCmd) Run() error {
	dir, err := filepath.Abs(c.Dir)
	if err != nil {
		return err
	}
	m, _, err := loadManifest(dir)
	if err != nil {
		if !c.Write || !errors.Is(err, os.ErrNotExist) {
			return err
		}
		m = defaultManifest()
	}
	for i := range m.Files {
		mf := &m.Files[i]
		src := filepath.Join(dir, m.TemplateDir, mf.Path)
		switch mf.Format {
		case "trust-dir":
			mf.Keys = nil
		case "env":
			data, err := os.ReadFile(src)
			if err != nil {
				return fmt.Errorf("template %s: %w", mf.Path, err)
			}
			mf.Keys = configmerge.EnvKeys(data)
		case "json", "jsonc":
			data, err := os.ReadFile(src)
			if err != nil {
				return fmt.Errorf("template %s: %w", mf.Path, err)
			}
			if mf.Format == "jsonc" {
				data = stripJSONComments(data)
			}
			mf.Keys = configmerge.JSONTopKeys(data)
		default:
			return fmt.Errorf("manifest %s: unknown format %q", mf.ID, mf.Format)
		}
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if !c.Write {
		fmt.Print(string(out))
		return nil
	}
	target := filepath.Join(dir, manifestName)
	if err := writeAtomic(target, out); err != nil {
		return err
	}
	fmt.Println("wrote " + target)
	return nil
}
