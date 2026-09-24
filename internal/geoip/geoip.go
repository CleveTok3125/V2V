// Package geoip adapts MaxMind-format .mmdb files (GeoLite2 or
// compatible DB-IP Lite, operator-supplied) to the behavior.Resolver
// interface. The directory is optional: a missing or empty directory
// yields a nil resolver and ASN/region grouping quietly degrades to
// IP+subnet. A present-but-corrupt database is a boot error.
package geoip

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/oschwald/geoip2-golang"

	"github.com/CleveTok3125/V2V/internal/behavior"
)

// Conventional database filenames. DB-IP Lite files are format
// compatible; rename them to these names.
const (
	asnFile     = "GeoLite2-ASN.mmdb"
	cityFile    = "GeoLite2-City.mmdb"
	countryFile = "GeoLite2-Country.mmdb"
)

// Resolver wraps optional ASN and city/country readers. A nil
// *Resolver is valid: Lookup reports unknown for every address.
type Resolver struct {
	asn *geoip2.Reader
	geo *geoip2.Reader
}

// Open loads the databases found in dir. Missing/empty dir returns a
// nil resolver with a warning and no error.
func Open(dir string) (*Resolver, []string, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, []string{fmt.Sprintf("geoip: %s missing; ASN/region grouping disabled", dir)}, nil
	}
	r := &Resolver{}
	try := func(name string) (*geoip2.Reader, error) {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			return nil, nil
		}
		rd, err := geoip2.Open(path)
		if err != nil {
			return nil, fmt.Errorf("geoip: %s: %w", path, err)
		}
		return rd, nil
	}
	var err error
	if r.asn, err = try(asnFile); err != nil {
		return nil, nil, err
	}
	if r.geo, err = try(cityFile); err != nil {
		r.Close()
		return nil, nil, err
	} else if r.geo == nil {
		if r.geo, err = try(countryFile); err != nil {
			r.Close()
			return nil, nil, err
		}
	}
	if r.asn == nil && r.geo == nil {
		return nil, []string{fmt.Sprintf("geoip: no database in %s; ASN/region grouping disabled", dir)}, nil
	}
	return r, nil, nil
}

// Close releases open readers; nil-safe.
func (r *Resolver) Close() {
	if r == nil {
		return
	}
	if r.asn != nil {
		r.asn.Close()
	}
	if r.geo != nil {
		r.geo.Close()
	}
}

// Lookup resolves one IP string. A nil resolver or unknown address
// reports false; callers fall back to IP+subnet grouping.
func (r *Resolver) Lookup(ip string) (behavior.GeoInfo, bool) {
	var out behavior.GeoInfo
	if r == nil {
		return out, false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return out, false
	}
	found := false
	if r.asn != nil {
		if rec, err := r.asn.ASN(parsed); err == nil && rec.AutonomousSystemNumber != 0 {
			out.ASN = uint32(rec.AutonomousSystemNumber)
			found = true
		}
	}
	if r.geo != nil {
		if rec, err := r.geo.City(parsed); err == nil {
			if rec.Country.IsoCode != "" {
				out.Country = rec.Country.IsoCode
				found = true
			}
			if len(rec.Subdivisions) > 0 && rec.Subdivisions[0].IsoCode != "" {
				out.Region = rec.Subdivisions[0].IsoCode
				found = true
			}
		}
	}
	return out, found
}
