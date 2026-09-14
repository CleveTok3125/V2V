package trustedproxy

import (
	"net"
	"net/http"
	"strings"
)

// cloudflare trusts CF-Connecting-IP, but only from the operator's
// configured edge set. Any other address sending that header is a
// spoof attempt (reject). The header value must parse as an IP.
func init() {
	Register("cloudflare", func(trust []*net.IPNet) Provider {
		return &cloudflare{trust: trust}
	})
}

type cloudflare struct {
	trust []*net.IPNet
}

func (*cloudflare) Name() string                   { return "cloudflare" }
func (*cloudflare) HeaderTrust() bool              { return true }
func (*cloudflare) NeedsFile() bool                { return true }
func (c *cloudflare) Trusted(remoteIP string) bool { return containsIP(c.trust, remoteIP) }

func (*cloudflare) ClientIP(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))
	if raw == "" {
		return "", false
	}
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String(), true
	}
	return "", false
}
