package behavior

import (
	"testing"
	"time"
)

func TestProfileRingsCap(t *testing.T) {
	p := &Profile{}
	base := time.Now()
	for i := 0; i < maxRing+100; i++ {
		p.AddMessage(base.Add(time.Duration(i) * time.Second))
	}
	if len(p.Msgs) != maxRing {
		t.Fatalf("ring must cap at %d, got %d", maxRing, len(p.Msgs))
	}
	if p.TotalMsgs != int64(maxRing+100) {
		t.Fatalf("counter must not cap, got %d", p.TotalMsgs)
	}
}

func TestProfileFirstLastSeen(t *testing.T) {
	p := &Profile{}
	if !p.FirstSeen.IsZero() {
		t.Fatal("fresh profile must have zero FirstSeen")
	}
	a := time.Now()
	p.AddConnect(a)
	if !p.FirstSeen.Equal(a) || !p.LastSeen.Equal(a) {
		t.Fatal("connect must stamp first and last seen")
	}
	b := a.Add(time.Hour)
	p.AddMessage(b)
	if !p.LastSeen.Equal(b) {
		t.Fatal("message must advance LastSeen")
	}
	if !p.FirstSeen.Equal(a) {
		t.Fatal("FirstSeen must stay pinned")
	}
}

func TestProfileIdentityStrongestWins(t *testing.T) {
	p := &Profile{}
	p.SetIdentity(IdGuest)
	p.SetIdentity(IdTrip)
	if p.Identity != IdTrip {
		t.Fatal("trip must outrank guest")
	}
	p.SetIdentity(IdGuest)
	if p.Identity != IdTrip {
		t.Fatal("guest must not demote trip")
	}
	p.SetIdentity(IdKey)
	if p.Identity != IdKey {
		t.Fatal("key must outrank trip")
	}
}

func TestProfileHTTPAndErr(t *testing.T) {
	p := &Profile{}
	now := time.Now()
	p.AddHTTP(HTTPEvent{Time: now, Class: "trip_verify", Status: 429})
	p.AddErr(ErrEvent{Time: now, Code: "invalid_role"})
	if len(p.HTTP) != 1 || len(p.Errs) != 1 {
		t.Fatal("http and err events must append")
	}
	p.SetBlocklisted()
	if !p.Blocklisted {
		t.Fatal("blocklist flag must stick")
	}
}
