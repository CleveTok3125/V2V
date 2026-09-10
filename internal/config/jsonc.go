package config

import "errors"

// errUnterminatedComment reports a block comment without a closing */.
var errUnterminatedComment = errors.New("unterminated block comment")

// StripComments removes // line comments and /* */ block comments that
// appear outside JSON string literals. Only comments are handled:
// trailing commas and other JSONC extensions stay invalid on purpose.
// The returned slice aliases data; no copy is made.
func StripComments(data []byte) ([]byte, error) {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		c := data[i]
		if c == '"' {
			j := i + 1
			for j < len(data) {
				if data[j] == '\\' {
					j += 2
					continue
				}
				if data[j] == '"' {
					break
				}
				j++
			}
			if j >= len(data) {
				return nil, errors.New("unterminated string")
			}
			out = append(out, data[i:j+1]...)
			i = j + 1
			continue
		}
		if c == '/' && i+1 < len(data) {
			switch data[i+1] {
			case '/':
				for i < len(data) && data[i] != '\n' {
					i++
				}
				continue
			case '*':
				j := i + 2
				for j+1 < len(data) && !(data[j] == '*' && data[j+1] == '/') {
					j++
				}
				if j+1 >= len(data) {
					return nil, errUnterminatedComment
				}
				i = j + 2
				continue
			}
		}
		out = append(out, c)
		i++
	}
	return out, nil
}
