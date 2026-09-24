package behavior

import "testing"

type stubResolver struct {
	info map[string]GeoInfo
}

func (s stubResolver) Lookup(ip string) (GeoInfo, bool) {
	g, ok := s.info[ip]
	return g, ok
}

func TestGroupKeysV4(t *testing.T) {
	keys := GroupKeys("203.0.113.7")
	if len(keys) != 1 || keys[0] != "ip4/24:203.0.113.0" {
		t.Fatalf("got %v", keys)
	}
}

func TestGroupKeysV6(t *testing.T) {
	keys := GroupKeys("2001:db8:abcd:12::1")
	want64 := "ip6/64:2001:db8:abcd:12::"
	want48 := "ip6/48:2001:db8:abcd::"
	if len(keys) != 2 || keys[0] != want64 || keys[1] != want48 {
		t.Fatalf("got %v want [%s %s]", keys, want64, want48)
	}
}

func TestGroupKeysInvalid(t *testing.T) {
	if keys := GroupKeys("not-an-ip"); keys != nil {
		t.Fatalf("invalid IP must yield no keys, got %v", keys)
	}
	if keys := GroupKeys(""); keys != nil {
		t.Fatalf("empty IP must yield no keys, got %v", keys)
	}
}

func TestGeoKeys(t *testing.T) {
	r := stubResolver{info: map[string]GeoInfo{
		"203.0.113.7": {ASN: 64500, Country: "VN", Region: "SG"},
	}}
	keys := GeoKeys(r, "203.0.113.7")
	want := map[string]bool{"asn:64500": true, "country:VN": true, "region:SG": true}
	if len(keys) != 3 {
		t.Fatalf("got %v", keys)
	}
	for _, k := range keys {
		if !want[k] {
			t.Fatalf("unexpected key %s in %v", k, keys)
		}
	}
	if keys := GeoKeys(nil, "203.0.113.7"); keys != nil {
		t.Fatalf("nil resolver must yield no keys, got %v", keys)
	}
	if keys := GeoKeys(r, "198.51.100.9"); keys != nil {
		t.Fatalf("unknown IP must yield no keys, got %v", keys)
	}
}
