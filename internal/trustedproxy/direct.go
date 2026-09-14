package trustedproxy

import (
	"net"
	"net/http"
)

// direct marks operator-owned addresses (office, health checks) as
// plain direct clients. It never reads headers: an entry here grants
// direct access only, never the power to assert another IP. A
// misplaced non-CF address is therefore harmless, unlike the same
// mistake in a header provider's file.
func init() {
	Register("direct", func(trust []*net.IPNet) Provider {
		return &direct{trust: trust}
	})
}

type direct struct {
	trust []*net.IPNet
}

func (*direct) Name() string                   { return "direct" }
func (*direct) HeaderTrust() bool              { return false }
func (*direct) NeedsFile() bool                { return false }
func (d *direct) Trusted(remoteIP string) bool { return containsIP(d.trust, remoteIP) }

// ClientIP is unreachable: the chain only calls it on header
// providers. Fail closed if that ever changes.
func (*direct) ClientIP(_ *http.Request) (string, bool) { return "", false }
