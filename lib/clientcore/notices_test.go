package clientcore

import (
	"testing"

	"github.com/CleveTok3125/V2V/lib/wire"
)

// TestNotifyTagsForWire: a system line is gated on its tags, chat is never
// gated, and a notice from a peer that predates tagging still falls under
// the system switch instead of escaping it.
func TestTagsForWire(t *testing.T) {
	cases := []struct {
		name string
		msg  wire.WireMessage
		want string
	}{
		{"chat", wire.WireMessage{Type: "chat", Text: "hi"}, ""},
		{"join", wire.WireMessage{Type: "system", Tags: wire.WithTags(wire.TagJoin)}, wire.TagJoin},
		{"leave", wire.WireMessage{Type: "system", Tags: wire.WithTags(wire.TagLeave)}, wire.TagLeave},
		{"date", wire.WireMessage{Type: "system", Tags: wire.WithTags(wire.TagDate)}, wire.TagDate},
		{"limit", wire.WireMessage{Type: "system", Tags: wire.WithTags(wire.TagLimit)}, wire.TagLimit},
		{"screening", wire.WireMessage{Type: "system", Tags: wire.WithTags(wire.TagPowScreen)}, wire.TagPowScreen},
		// No tag: the root still applies, so the system switch covers it.
		{"untagged", wire.WireMessage{Type: "system", Text: "x"}, wire.TagRoot},
	}
	for _, tc := range cases {
		got := TagsForWire(tc.msg)
		if tc.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: chat must not be gated, got %v", tc.name, got)
			}
			continue
		}
		if !wire.HasTag(got, tc.want) {
			t.Errorf("%s: notifyTagsForWire = %v, want it to carry %q", tc.name, got, tc.want)
		}
	}
}

func TestNoticesTagsAllowedGatesOnAncestors(t *testing.T) {
	n := Notices{Muted: map[string]bool{wire.TagPow: true}}
	if n.TagsAllowed(wire.WithTags(wire.TagPowScreen)) {
		t.Fatal("muting a parent must hide its subtree")
	}
	if !n.TagsAllowed(wire.WithTags(wire.TagJoin)) {
		t.Fatal("an unrelated chain must stay live")
	}
	if !n.TagsAllowed(nil) {
		t.Fatal("untagged chat is never gated")
	}
}

func TestNoticesPowGate(t *testing.T) {
	n := Notices{Muted: map[string]bool{}, PowMinTier: 3}
	if !n.PowAllowed(3) || n.PowAllowed(2) {
		t.Fatalf("floor must be inclusive: %v %v", n.PowAllowed(3), n.PowAllowed(2))
	}
	if !n.PowLive(3) || n.PowLive(2) {
		t.Fatalf("PowLive must follow the floor: %v %v", n.PowLive(3), n.PowLive(2))
	}
	muted := Notices{Muted: map[string]bool{wire.TagPow: true}, PowMinTier: 1}
	if muted.PowLive(9) {
		t.Fatal("muting the pow chain must hide the solving line at any tier")
	}
}

func TestNoticesWireAllowedTierlessIsNeverFiltered(t *testing.T) {
	n := Notices{Muted: map[string]bool{}, PowMinTier: 3}
	notice := func(tier int) wire.WireMessage {
		return wire.WireMessage{
			Type: "system", Tags: wire.WithTags(wire.TagPowScreen), SysPowTier: tier,
		}
	}
	if !n.WireAllowed(notice(0)) {
		t.Fatal("a tierless notice is a deadline or a result, not a challenge")
	}
	if n.WireAllowed(notice(2)) || !n.WireAllowed(notice(3)) {
		t.Fatal("a challenge must respect the floor")
	}
	chat := wire.WireMessage{Type: "chat", Text: "hi"}
	if !n.WireAllowed(chat) {
		t.Fatal("chat is never gated")
	}
}
