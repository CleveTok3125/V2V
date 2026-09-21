package configmerge

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// EnvResult is the overlay outcome for .env files.
type EnvResult struct {
	Values          map[string]string
	Added           []string
	Kept            []string
	ChangedUpstream []string
	Orphaned        []string
	Order           []string
	Comments        map[string][]string
	LocalRaw        map[string]string
	NewRaw          map[string]string
	// Activated holds template-commented defaults that local enables with
	// an active value. They render uncommented at template position.
	Activated map[string]string
	// LocalCommented holds local raw "#KEY=value" lines, so an added key
	// rendered as a comment can preserve the operator's own commented value.
	LocalCommented map[string]string
}

// TrustResult is the union merge outcome for trust *.txt files.
type TrustResult struct {
	Entries  []string
	Added    []string
	Kept     []string
	Orphaned []string
}

// JSONResult merges top-level object keys (roles, client sections).
type JSONResult struct {
	Values     map[string]string
	Added      []string
	Kept       []string
	Overridden []string
	Orphaned   []string
	Object     map[string]any
}

func parseEnvLine(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	rest := trimmed
	if strings.HasPrefix(rest, "export ") {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "export "))
	}
	eq := strings.Index(rest, "=")
	if eq < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(rest[:eq])
	val := strings.TrimSpace(rest[eq+1:])
	if key == "" {
		return "", "", false
	}
	for _, r := range key {
		if r == ' ' || r == '\t' || r == '#' {
			return "", "", false
		}
	}
	value = unquoteEnv(val)
	return key, value, true
}

func unquoteEnv(val string) string {
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
			inner := val[1 : len(val)-1]
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\'`, `'`)
			return inner
		}
	}
	// Unquoted values may carry a trailing comment; quoted ones never do.
	if idx := strings.Index(val, " #"); idx >= 0 {
		return strings.TrimSpace(val[:idx])
	}
	return val
}

// ParseEnvLine parses one .env line for external verification.
func ParseEnvLine(line string) (key, value string, ok bool) {
	return parseEnvLine(line)
}

// parseCommentedEnvLine recognizes disabled defaults like "# DATA_DIR=./data".
// Prose comments without a valid KEY= assignment report ok=false.
func parseCommentedEnvLine(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	return parseEnvLine(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
}

// envFile is one parsed .env file. values holds active entries;
// commented holds disabled "# KEY=val" defaults.
type envFile struct {
	order         []string
	values        map[string]string
	raws          map[string]string
	comments      map[string][]string
	commented     map[string]string
	commentedRaws map[string]string
}

func parseEnv(data []byte) envFile {
	f := envFile{
		values:        map[string]string{},
		raws:          map[string]string{},
		comments:      map[string][]string{},
		commented:     map[string]string{},
		commentedRaws: map[string]string{},
	}
	var pending []string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(pending) > 0 && len(f.order) == 0 {
				pending = append(pending, line)
			}
			continue
		}
		if key, value, ok := parseEnvLine(line); ok {
			if _, seen := f.values[key]; !seen {
				f.order = append(f.order, key)
			}
			f.values[key] = value
			f.raws[key] = line
			if len(pending) > 0 {
				f.comments[key] = append([]string(nil), pending...)
				pending = nil
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if key, value, ok := parseCommentedEnvLine(line); ok {
				if _, inActive := f.values[key]; !inActive {
					if _, seen := f.commented[key]; !seen {
						f.commented[key] = value
						f.commentedRaws[key] = line
					}
				}
			}
			pending = append(pending, line)
			continue
		}
		pending = nil
	}
	return f
}

// RenderEnv walks the new template lines verbatim, substituting merged
// values into active entries and activating commented defaults that local
// enables. Comments, blank lines and quoting style stay byte-identical, so
// merging a template with itself reproduces it exactly.
func RenderEnv(res EnvResult, newData []byte) []byte {
	return RenderEnvWithPolicy(res, newData, nil)
}

// RenderEnvWithPolicy renders like RenderEnv, but keys in commentOnAdd that
// are newly added render as commented defaults ("#KEY=value") instead of
// active entries. When local already carries a commented raw line for the
// key, that raw line wins so the operator's own value survives.
func RenderEnvWithPolicy(res EnvResult, newData []byte, commentOnAdd map[string]bool) []byte {
	lines := strings.Split(string(newData), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out []string
	emitted := map[string]bool{}
	for _, line := range lines {
		if key, _, ok := parseEnvLine(line); ok {
			emitted[key] = true
			if commentOnAdd[key] && containsStr(res.Added, key) {
				if raw, ok := res.LocalCommented[key]; ok {
					out = append(out, raw)
				} else {
					out = append(out, commentEnvLine(line))
				}
				continue
			}
			if val, inVals := res.Values[key]; inVals {
				out = append(out, substituteEnvValue(line, val))
			} else {
				out = append(out, line)
			}
			continue
		}
		if key, _, ok := parseCommentedEnvLine(line); ok {
			if val, act := res.Activated[key]; act {
				out = append(out, activateEnvLine(line, val))
				emitted[key] = true
			} else {
				out = append(out, line)
			}
			continue
		}
		out = append(out, line)
	}
	for _, key := range res.Order {
		if emitted[key] {
			continue
		}
		val, ok := res.Values[key]
		if !ok {
			continue
		}
		if c, ok := res.Comments[key]; ok {
			out = append(out, c...)
		}
		if raw, ok := res.LocalRaw[key]; ok {
			out = append(out, raw)
		} else {
			out = append(out, key+"="+val)
		}
		emitted[key] = true
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// substituteEnvValue replaces the value of one raw entry line, preserving
// the key prefix, quoting style and any trailing comment. Identical values
// return the raw line untouched.
func substituteEnvValue(raw, val string) string {
	_, oldVal, ok := parseEnvLine(raw)
	if !ok {
		return raw
	}
	if oldVal == val {
		return raw
	}
	eq := strings.Index(raw, "=")
	if eq < 0 {
		return raw
	}
	prefix := raw[:eq+1]
	rest := raw[eq+1:]
	trailer := ""
	if i := unquotedHash(rest); i >= 0 {
		trailer = strings.TrimSpace(rest[i:])
		rest = rest[:i]
	}
	restTrim := strings.TrimSpace(rest)
	quote := ""
	if len(restTrim) >= 2 && ((restTrim[0] == '"' && restTrim[len(restTrim)-1] == '"') || (restTrim[0] == '\'' && restTrim[len(restTrim)-1] == '\'')) {
		quote = restTrim[:1]
	}
	var styled string
	switch {
	case quote == `"`:
		styled = `"` + strings.ReplaceAll(val, `"`, `\"`) + `"`
	case quote == `'`:
		styled = `'` + strings.ReplaceAll(val, `'`, `\'`) + `'`
	case needsQuoting(val):
		styled = `"` + strings.ReplaceAll(val, `"`, `\"`) + `"`
	default:
		styled = val
	}
	if trailer != "" {
		return prefix + styled + " " + trailer
	}
	return prefix + styled
}

// activateEnvLine turns a commented default line into an active entry with
// the merged value, preserving indentation.
func activateEnvLine(raw, val string) string {
	i := 0
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
		i++
	}
	indent := raw[:i]
	rest := strings.TrimLeft(strings.TrimLeft(raw[i:], "#"), " \t")
	if _, _, ok := parseEnvLine(rest); !ok {
		return raw
	}
	return indent + substituteEnvValue(rest, val)
}

// unquotedHash reports the index of a " #" comment starter outside quotes.
func unquotedHash(s string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
			return i
		}
	}
	return -1
}

func needsQuoting(val string) bool {
	return strings.ContainsAny(val, " \t#\"'")
}

// commentEnvLine comments an active entry line, preserving indentation.
// The "#" sits directly against the key ("#KEY=value") per template
// convention.
func commentEnvLine(raw string) string {
	i := 0
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
		i++
	}
	return raw[:i] + "#" + raw[i:]
}

// EnvKeys returns the active key order of one .env document.
func EnvKeys(data []byte) []string {
	return parseEnv(data).order
}

// JSONTopKeys returns the top-level key order of one JSON/JSONC document.
func JSONTopKeys(data []byte) []string {
	_, orders := scanJSONLayout(data)
	return orders[""]
}

func containsStr(list []string, key string) bool {
	for _, v := range list {
		if v == key {
			return true
		}
	}
	return false
}

func normalizeTrustLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	return strings.ToLower(trimmed), true
}

func trustSet(data []byte) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if e, ok := normalizeTrustLine(line); ok {
			set[e] = true
		}
	}
	return set
}

func trustOrder(data []byte) []string {
	var order []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		e, ok := normalizeTrustLine(line)
		if !ok || seen[e] {
			continue
		}
		seen[e] = true
		order = append(order, e)
	}
	return order
}

// RenderTrust walks the template lines verbatim, keeping comments, blank
// lines and examples in place. Entries present in the merged set stay;
// local-only entries append at the end. Merging a template with itself
// reproduces it exactly.
func RenderTrust(res TrustResult, newData []byte) []byte {
	inMerged := map[string]bool{}
	for _, e := range res.Entries {
		inMerged[e] = true
	}
	lines := strings.Split(string(newData), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out []string
	emitted := map[string]bool{}
	for _, line := range lines {
		if e, ok := normalizeTrustLine(line); ok {
			if inMerged[e] {
				out = append(out, line)
				emitted[e] = true
			}
			continue
		}
		out = append(out, line)
	}
	for _, e := range res.Entries {
		if emitted[e] {
			continue
		}
		out = append(out, e)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func canonicalJSON(data []byte) (map[string]any, map[string]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return map[string]any{}, map[string]string{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return nil, nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	canonical := map[string]string{}
	for k, v := range root {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		canonical[k] = string(raw)
	}
	return root, canonical, nil
}

func nonEmptyJSON(data []byte) []byte {
	if len(bytes.TrimSpace(data)) == 0 {
		return []byte("{}")
	}
	return data
}

// RenderJSON marshals the merged object following the template layout:
// indent unit and key order come from templateData, new top-level keys sort
// last. Merging a template with itself reproduces it exactly.
func RenderJSON(res JSONResult, templateData []byte) ([]byte, error) {
	indent, orders := scanJSONLayout(templateData)
	top := append([]string(nil), orders[""]...)
	seen := map[string]bool{}
	for _, k := range top {
		seen[k] = true
	}
	var rest []string
	for k := range res.Object {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	top = append(top, rest...)
	out := marshalOrdered(res.Object, top, orders, "", indent, 0)
	return []byte(out + "\n"), nil
}

// scanJSONLayout extracts the indent unit and per-object key order from raw
// JSON text. Strings and // /* */ comments are skipped so comment-looking
// payload never pollutes the layout.
func scanJSONLayout(data []byte) (string, map[string][]string) {
	orders := map[string][]string{}
	type frame struct {
		path       string
		isObj      bool
		expectKey  bool
		pending    string
		hasPending bool
	}
	var stack []frame
	minIndent := -1
	indent := ""
	i, n := 0, len(data)
	atLineStart := true
	ws := ""
	for i < n {
		c := data[i]
		if atLineStart && (c == ' ' || c == '\t') {
			ws += string([]byte{c})
			i++
			continue
		}
		atLineStart = false
		switch {
		case c == '\n':
			ws = ""
			atLineStart = true
			i++
			continue
		case c == '/' && i+1 < n && data[i+1] == '/':
			for i < n && data[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < n && data[i+1] == '*':
			i += 2
			for i+1 < n && !(data[i] == '*' && data[i+1] == '/') {
				if data[i] == '\n' {
					atLineStart = true
					ws = ""
				}
				i++
			}
			i += 2
			continue
		case c == '"':
			j := i + 1
			esc := false
			for j < n {
				if esc {
					esc = false
				} else if data[j] == '\\' {
					esc = true
				} else if data[j] == '"' {
					break
				}
				j++
			}
			if j >= n {
				i = n
				continue
			}
			str := string(data[i+1 : j])
			i = j + 1
			if len(stack) > 0 && stack[len(stack)-1].isObj && stack[len(stack)-1].expectKey {
				k := i
				for k < n && (data[k] == ' ' || data[k] == '\t' || data[k] == '\n' || data[k] == '\r') {
					k++
				}
				if k < n && data[k] == ':' {
					top := &stack[len(stack)-1]
					top.pending = str
					top.hasPending = true
					top.expectKey = false
					orders[top.path] = append(orders[top.path], str)
					if ws != "" && (minIndent < 0 || len(ws) < minIndent) {
						minIndent = len(ws)
						indent = ws
					}
					i = k + 1
					continue
				}
			}
			continue
		case c == '{':
			path := ""
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				if top.isObj && top.hasPending {
					path = joinJSONPath(top.path, top.pending)
					top.hasPending = false
					stack[len(stack)-1] = top
				} else if !top.isObj {
					path = top.path + "[]"
				}
			}
			stack = append(stack, frame{path: path, isObj: true, expectKey: true})
			i++
			continue
		case c == '[':
			path := ""
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				if top.isObj && top.hasPending {
					path = joinJSONPath(top.path, top.pending)
					top.hasPending = false
					stack[len(stack)-1] = top
				} else if !top.isObj {
					path = top.path + "[]"
				}
			}
			stack = append(stack, frame{path: path, isObj: false})
			i++
			continue
		case c == '}' || c == ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if len(stack) > 0 && stack[len(stack)-1].isObj {
				stack[len(stack)-1] = frame{path: stack[len(stack)-1].path, isObj: true, expectKey: true}
			}
			i++
			continue
		case c == ',':
			if len(stack) > 0 && stack[len(stack)-1].isObj {
				top := stack[len(stack)-1]
				top.expectKey = true
				top.hasPending = false
				stack[len(stack)-1] = top
			}
			i++
			continue
		default:
			i++
		}
		ws = ""
	}
	if indent == "" {
		indent = "  "
	}
	return indent, orders
}

func joinJSONPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// marshalOrdered renders v with template key order and indent unit.
func marshalOrdered(v any, order []string, orders map[string][]string, path, indent string, level int) string {
	switch tv := v.(type) {
	case map[string]any:
		if len(tv) == 0 {
			return "{}"
		}
		keys := orderedKeys(tv, order)
		var b strings.Builder
		pad := strings.Repeat(indent, level+1)
		closePad := strings.Repeat(indent, level)
		b.WriteString("{\n")
		for i, k := range keys {
			keyJSON, _ := marshalScalar(k)
			child := marshalOrdered(tv[k], orders[joinJSONPath(path, k)], orders, joinJSONPath(path, k), indent, level+1)
			b.WriteString(pad + keyJSON + ": " + child)
			if i < len(keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(closePad + "}")
		return b.String()
	case []any:
		if len(tv) == 0 {
			return "[]"
		}
		var b strings.Builder
		pad := strings.Repeat(indent, level+1)
		closePad := strings.Repeat(indent, level)
		b.WriteString("[\n")
		for i, e := range tv {
			b.WriteString(pad + marshalOrdered(e, orders[path+"[]"], orders, path+"[]", indent, level+1))
			if i < len(tv)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(closePad + "]")
		return b.String()
	default:
		raw, _ := marshalScalar(tv)
		return raw
	}
}

// marshalScalar encodes one JSON value without HTML escaping, so template
// text like "| > " stays literal instead of becoming "\u003e".
func marshalScalar(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func orderedKeys(m map[string]any, order []string) []string {
	seen := map[string]bool{}
	var keys []string
	for _, k := range order {
		if _, ok := m[k]; ok && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	var rest []string
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// OverlayEnv overlays local values onto the new template structure.
// The template wins structure, order and comments; local wins values.
// Report groups: added (new-only), kept (same), overridden (both, differ,
// local kept), orphaned (local-only, kept). No conflicts exist in 2-way.
func OverlayEnv(localData, newData []byte) EnvResult {
	local := parseEnv(localData)
	localVals, localRaws, localComments := local.values, local.raws, local.comments
	newf := parseEnv(newData)
	newOrder, newVals, newRaws, newComments := newf.order, newf.values, newf.raws, newf.comments
	res := EnvResult{
		Values:         map[string]string{},
		Comments:       map[string][]string{},
		LocalRaw:       localRaws,
		NewRaw:         newRaws,
		Activated:      map[string]string{},
		LocalCommented: local.commentedRaws,
	}
	for _, key := range newOrder {
		newVal := newVals[key]
		localVal, localHas := localVals[key]
		switch {
		case !localHas:
			res.Values[key] = newVal
			res.Added = append(res.Added, key)
		case localVal == newVal:
			res.Values[key] = localVal
			res.Kept = append(res.Kept, key)
		default:
			res.Values[key] = localVal
			res.Kept = append(res.Kept, key)
			res.ChangedUpstream = append(res.ChangedUpstream, key)
		}
	}
	localOnly := []string{}
	for key, localVal := range localVals {
		if _, inNew := newVals[key]; inNew {
			continue
		}
		if _, inNewC := newf.commented[key]; inNewC {
			continue
		}
		res.Values[key] = localVal
		res.Orphaned = append(res.Orphaned, key)
		res.Kept = append(res.Kept, key)
		localOnly = append(localOnly, key)
	}
	for key := range newf.commented {
		localVal, localHas := localVals[key]
		if !localHas {
			continue
		}
		if _, done := res.Values[key]; done {
			continue
		}
		res.Values[key] = localVal
		res.Activated[key] = localVal
		res.Kept = append(res.Kept, key)
		res.ChangedUpstream = append(res.ChangedUpstream, key)
	}
	sort.Strings(localOnly)
	res.Order = append([]string(nil), newOrder...)
	res.Order = append(res.Order, localOnly...)
	for _, key := range res.Order {
		if c, ok := newComments[key]; ok {
			res.Comments[key] = c
		} else if c, ok := localComments[key]; ok {
			res.Comments[key] = c
		}
	}
	sort.Strings(res.Added)
	sort.Strings(res.Kept)
	sort.Strings(res.ChangedUpstream)
	sort.Strings(res.Orphaned)
	return res
}

// OverlayTrust unions local entries onto the new template entry set.
func OverlayTrust(localData, newData []byte) TrustResult {
	localSet := trustSet(localData)
	newSet := trustSet(newData)
	localOrder := trustOrder(localData)
	newOrder := trustOrder(newData)
	seen := map[string]bool{}
	var entries []string
	var added, kept, orphaned []string
	for _, e := range newOrder {
		if seen[e] {
			continue
		}
		seen[e] = true
		entries = append(entries, e)
		if localSet[e] {
			kept = append(kept, e)
		} else {
			added = append(added, e)
		}
		_ = newSet
	}
	for _, e := range localOrder {
		if seen[e] {
			continue
		}
		seen[e] = true
		entries = append(entries, e)
		orphaned = append(orphaned, e)
		kept = append(kept, e)
	}
	sort.Strings(added)
	sort.Strings(kept)
	sort.Strings(orphaned)
	return TrustResult{Entries: entries, Added: added, Kept: kept, Orphaned: orphaned}
}

// OverlayJSON overlays local top-level keys onto the new template object.
// Local wins values; new wins structure for shared docs. Overridden lists
// keys present in both with different canonical values (local kept).
func OverlayJSON(localData, newData []byte) JSONResult {
	return OverlayJSONWithSkip(localData, newData, nil)
}

// OverlayJSONWithSkip behaves like OverlayJSON but never copies a newly
// added top-level key listed in skipOnAdd. Keys the operator already
// defines locally are unaffected.
func OverlayJSONWithSkip(localData, newData []byte, skipOnAdd map[string]bool) JSONResult {
	localRoot, localVals, _ := canonicalJSON(nonEmptyJSON(localData))
	newRoot, newVals, _ := canonicalJSON(nonEmptyJSON(newData))
	if localRoot == nil {
		localRoot = map[string]any{}
	}
	if newRoot == nil {
		newRoot = map[string]any{}
	}
	if localVals == nil {
		localVals = map[string]string{}
	}
	if newVals == nil {
		newVals = map[string]string{}
	}
	res := JSONResult{Values: map[string]string{}, Object: map[string]any{}}
	for key, newVal := range newVals {
		localVal, localHas := localVals[key]
		switch {
		case !localHas:
			if skipOnAdd[key] {
				continue
			}
			res.Values[key] = newVal
			res.Object[key] = newRoot[key]
			res.Added = append(res.Added, key)
		case localVal == newVal:
			res.Values[key] = localVal
			res.Object[key] = localRoot[key]
			res.Kept = append(res.Kept, key)
		default:
			res.Values[key] = localVal
			res.Object[key] = localRoot[key]
			res.Kept = append(res.Kept, key)
			res.Overridden = append(res.Overridden, key)
		}
	}
	for key, localVal := range localVals {
		if _, inNew := newVals[key]; inNew {
			continue
		}
		res.Values[key] = localVal
		res.Object[key] = localRoot[key]
		res.Orphaned = append(res.Orphaned, key)
		res.Kept = append(res.Kept, key)
	}
	sort.Strings(res.Added)
	sort.Strings(res.Kept)
	sort.Strings(res.Overridden)
	sort.Strings(res.Orphaned)
	return res
}
