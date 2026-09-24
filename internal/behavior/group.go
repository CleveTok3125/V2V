package behavior

import (
	"fmt"
	"net"
	"strings"
)

// GroupKeys returns the subnet aggregation keys for an IP: IPv4 /24,
// IPv6 /64 and /48. The /48 catches rotation across a subscriber's
// whole site when /64 alone would let them slip subnets. Invalid input
// yields no keys.
func GroupKeys(ip string) []string {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return nil
	}
	if v4 := parsed.To4(); v4 != nil {
		return []string{"ip4/24:" + v4.Mask(net.CIDRMask(24, 32)).String()}
	}
	mask := func(bits int) string {
		return parsed.Mask(net.CIDRMask(bits, 128)).String()
	}
	return []string{"ip6/64:" + mask(64), "ip6/48:" + mask(48)}
}

// GeoInfo is one resolved address: ASN, ISO country and region.
type GeoInfo struct {
	ASN     uint32
	Country string
	Region  string
}

// Resolver maps an IP string to geo data. A nil resolver (or unknown
// IP) simply yields no keys, so ASN/region grouping degrades to
// IP+subnet automatically.
type Resolver interface {
	Lookup(ip string) (GeoInfo, bool)
}

// GeoKeys appends ASN/country/region keys when the resolver knows the
// address. Empty country/region fields are skipped.
func GeoKeys(r Resolver, ip string) []string {
	if r == nil {
		return nil
	}
	g, ok := r.Lookup(ip)
	if !ok {
		return nil
	}
	var out []string
	if g.ASN != 0 {
		out = append(out, fmt.Sprintf("asn:%d", g.ASN))
	}
	if g.Country != "" {
		out = append(out, "country:"+strings.ToUpper(g.Country))
	}
	if g.Region != "" {
		out = append(out, "region:"+g.Region)
	}
	return out
}
