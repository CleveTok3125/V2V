package wire

import "strings"

// Notice tags: the single source of truth for what a line is, shared by
// the server that produces it and the client that filters it.
//
// A line carries the tags that apply, most general first, each a full
// dotted path from the root: muting "system" hides every notice,
// muting "system.pow" hides the PoW flow, muting
// "system.pow.screening" hides only that step. A line hides when any
// of its tags, or any ancestor of them, is muted.
//
// Tags are display metadata, not integrity proof: outside the chain
// hash, and a peer can strip or forge them.

// TagRoot is the only filterable root. Chat lines carry no tags and are
// never filtered.
const TagRoot = "system"

// Tag leaves and intermediates, as dotted paths from TagRoot.
const (
	TagJoin  = "system.join"
	TagLeave = "system.leave"
	TagDate  = "system.date"

	// History replay window markers.
	TagHistory          = "system.history"
	TagHistoryBegin     = "system.history.begin"
	TagHistoryOlder     = "system.history.older"
	TagHistoryRecover   = "system.history.recover"
	TagHistoryEnd       = "system.history.end"
	TagHistoryExhausted = "system.history.exhausted"

	TagPow       = "system.pow"
	TagPowGate   = "system.pow.gate"
	TagPowScreen = "system.pow.screening"

	// TagAudit marks a management line chained as evidence, so verify can
	// replay it. Reserved: no producer publishes on it yet, so the wire
	// shape settles before anything needs it.
	TagAudit = "system.audit"

	TagLimit    = "system.limit"
	TagEnvelope = "system.envelope"
	TagTrip     = "system.trip"
	// TagTripReveal marks the room-wide seed-reveal announcement: a child
	// of TagTrip, so it mutes without hiding the trip warnings.
	TagTripReveal = "system.trip.reveal"
	TagFilter     = "system.filter"
	TagAuth       = "system.auth"

	// TagVote marks a vote total broadcast: an unchained system line
	// carrying the cumulative up/down for one chain height. Clients
	// fold it into the target message score instead of printing it.
	TagVote = "system.vote"
)

// tagTree lists every known tag, parents before children.
var tagTree = []string{
	TagRoot,
	TagJoin,
	TagLeave,
	TagDate,
	TagHistory,
	TagHistoryBegin,
	TagHistoryOlder,
	TagHistoryRecover,
	TagHistoryEnd,
	TagHistoryExhausted,
	TagPow,
	TagPowGate,
	TagPowScreen,
	TagAudit,
	TagLimit,
	TagEnvelope,
	TagTrip,
	TagTripReveal,
	TagFilter,
	TagAuth,
	TagVote,
}

// AllTags returns every known tag, parents before children.
func AllTags() []string {
	out := make([]string, len(tagTree))
	copy(out, tagTree)
	return out
}

// knownAncestor returns the closest known tag enclosing this one, or "".
// It looks past unrecognized levels, so a leaf a newer peer published
// still resolves instead of escaping every gate above it.
func knownAncestor(tag string) string {
	for at := syntacticParent(tag); at != ""; at = syntacticParent(at) {
		if HasTag(tagTree, at) {
			return at
		}
	}
	return ""
}

// syntacticParent returns tag up to its last dot, whether or not that
// parent is part of the taxonomy.
func syntacticParent(tag string) string {
	if cut := strings.LastIndex(tag, "."); cut > 0 {
		return tag[:cut]
	}
	return ""
}

// knownChain returns tag and the known ancestors enclosing it, ordered from
// the root down and skipping levels the taxonomy does not define.
func knownChain(tag string) []string {
	var chain []string
	for at := tag; at != ""; at = knownAncestor(at) {
		chain = append([]string{at}, chain...)
	}
	return chain
}

// HasTag reports whether tags contains tag.
func HasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// HasAnyTag reports whether tags contains at least one of the wanted tags.
func HasAnyTag(tags []string, wanted ...string) bool {
	for _, w := range wanted {
		if HasTag(tags, w) {
			return true
		}
	}
	return false
}

// BlockedBy returns the muted keys hiding a line carrying tags: a key
// matches when it is one of the tags or an ancestor of one. Non-empty
// means hidden, which is what makes muting a node mute its subtree.
//
// Keys outside the taxonomy never match, so callers pass whatever gate
// names they hold. Results follow tag order, so the same input always
// yields the same output.
func BlockedBy(tags []string, muted map[string]bool) []string {
	if len(muted) == 0 || len(tags) == 0 {
		return nil
	}
	var blocked []string
	seen := make(map[string]bool, len(tags))
	// Every notice sits under the root, so test it even for a chain that
	// omits it: a peer publishing only "system.a.b" would otherwise stop
	// the walk at the first ancestor this build does not know and escape
	// a muted root.
	walk := func(tag string) {
		// knownChain reaches past a level this build does not know, so a
		// peer chain that skips a level is still matched against the
		// ancestors it does sit under.
		for _, at := range knownChain(tag) {
			if seen[at] {
				continue
			}
			seen[at] = true
			if muted[at] {
				blocked = append(blocked, at)
			}
		}
	}
	walk(TagRoot)
	for _, tag := range tags {
		walk(tag)
	}
	return blocked
}

// WithTags returns tags plus the ancestors each one implies, ordered from
// the root down so the result reads the way the tree does. Producers call
// it to publish a leaf without spelling out the whole chain by hand.
//
// Already-present tags are kept where they are, so the result of tagging
// the same line twice is stable.
func WithTags(leaves ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(tag string) {
		if seen[tag] {
			return
		}
		seen[tag] = true
		out = append(out, tag)
	}
	add(TagRoot)
	for _, leaf := range leaves {
		for _, t := range knownChain(leaf) {
			add(t)
		}
	}
	return out
}
