package guard

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// BuildBaseDisplayName is the pure part of generateDisplayName.
func BuildBaseDisplayName(name, ipSuffix string, prefix string) string {
	if prefix != "" {
		return prefix + name
	}
	return name + "#" + ipSuffix
}

func IPSuffix(ip string, salt []byte) string {
	h := hmac.New(sha256.New, salt)
	h.Write([]byte(ip))
	return hex.EncodeToString(h.Sum(nil))[:4]
}
