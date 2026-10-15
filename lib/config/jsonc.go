package config

import "github.com/tailscale/hujson"

// StripComments removes // and /* */ comments and trailing commas,
// returning standard JSON. Malformed input errors here, never in
// encoding/json.
//
// A missing final newline is added first: a // comment runs to end of
// line, so without it the parser reports EOF on inputs the old scanner
// (and sealed configs in the wild) accepts.
func StripComments(data []byte) ([]byte, error) {
	if len(data) > 0 && data[len(data)-1] != '\n' {
		buf := make([]byte, 0, len(data)+1)
		buf = append(buf, data...)
		buf = append(buf, '\n')
		data = buf
	}
	return hujson.Standardize(data)
}
