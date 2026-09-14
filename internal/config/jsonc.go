package config

import "github.com/tailscale/hujson"

// StripComments removes // line comments and /* */ block comments,
// returning standard JSON. Trailing commas are also accepted (standard
// JSONC behavior). Malformed input errors here instead of reaching
// encoding/json.
//
// A missing final newline is added before parsing: a // comment runs
// to end of line, so without it the parser would report unexpected EOF
// on inputs the old scanner (and sealed configs in the wild) accept.
func StripComments(data []byte) ([]byte, error) {
	if len(data) > 0 && data[len(data)-1] != '\n' {
		buf := make([]byte, 0, len(data)+1)
		buf = append(buf, data...)
		buf = append(buf, '\n')
		data = buf
	}
	return hujson.Standardize(data)
}
