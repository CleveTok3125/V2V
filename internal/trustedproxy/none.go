package trustedproxy

import (
	"net"
	"net/http"
)

// none disables proxy handling: every request resolves to RemoteAddr
// and all proxy headers are ignored. In a chain it is the final
// fallback (direct with a warning); alone it means no proxy at all.
func init() {
	Register("none", func(_ []*net.IPNet) Provider { return &none{} })
}

type none struct{}

func (*none) Name() string                            { return "none" }
func (*none) HeaderTrust() bool                       { return false }
func (*none) NeedsFile() bool                         { return false }
func (*none) Trusted(_ string) bool                   { return true }
func (*none) ClientIP(_ *http.Request) (string, bool) { return "", false }
