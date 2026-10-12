package clientcore

// The notice-gate policy: which tag chains are muted and the proof-of-work
// tier floor. It is pure — the caller owns the lock around its own copy.

import "github.com/CleveTok3125/V2V/lib/wire"

// Notices is the gate state.
type Notices struct {
	Muted      map[string]bool
	PowMinTier int
}

// TagsAllowed reports whether a line carrying these tags is live: no tag it
// carries, and no ancestor of one, is muted.
func (n Notices) TagsAllowed(tags []string) bool {
	return len(wire.BlockedBy(tags, n.Muted)) == 0
}

// PowAllowed reports whether a proof-of-work notice of this tier is at or
// above the tier floor. It is strict: a tier of zero means the notice carries
// none, and WireAllowed treats those as unfiltered rather than delegating
// here, because the floor says how much work to announce, not which outcome
// to hide.
func (n Notices) PowAllowed(tier int) bool { return tier >= n.PowMinTier }

// PowLive is PowAllowed plus the tag gate on the pow tag.
func (n Notices) PowLive(tier int) bool {
	return n.TagsAllowed([]string{wire.TagPow}) && n.PowAllowed(tier)
}

// WireAllowed reports whether a wire is live. The tier floor is checked here
// and not only where a client emits its own solving line, or ui.powMinTier
// would appear to work while a server-sent challenge below it still printed.
func (n Notices) WireAllowed(msg wire.WireMessage) bool {
	tags := TagsForWire(msg)
	if !n.TagsAllowed(tags) {
		return false
	}
	if !wire.HasTag(tags, wire.TagPow) {
		return true
	}
	if msg.SysPowTier <= 0 {
		return true
	}
	return n.PowAllowed(msg.SysPowTier)
}

// TagsForWire returns the tags a wire is gated on, or nil for content that is
// always live. Only system lines are notices; chat is never muted. An
// untagged system line falls back to the root, so a notice from a peer that
// predates tagging is still covered by the system switch.
func TagsForWire(msg wire.WireMessage) []string {
	if msg.Type != "system" {
		return nil
	}
	if len(msg.Tags) == 0 {
		return wire.WithTags(wire.TagRoot)
	}
	return msg.Tags
}
