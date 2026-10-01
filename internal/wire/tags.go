package wire

import "strings"

// Notice tags: the single source of truth for what a line is, shared by
// the server that produces it and the client that filters it.
//
// A line carries the tags that apply to it, most general first. Every tag
// is a full dotted path from the root, so a screening challenge is
// ["system","system.pow","system.pow.screening"]: muting "system" hides
// every notice, muting "system.pow" hides the PoW flow, and muting
// "system.pow.screening" hides only that step. A line is hidden when any
// of its tags, or any ancestor of them, is muted.
//
// Tags are display metadata, not integrity proof: they are not covered by
// the chain hash, and a peer can strip or forge them.

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

	// TagAudit marks a management line that is chained as evidence, so
	// verify can replay it. It is a leaf of the root: there is nothing
	// below it and nothing to group it with.
	TagAudit = "system.audit"

	TagLimit     = "system.limit"
	TagEnvelope  = "system.envelope"
	TagTrip      = "system.trip"
	TagFilter    = "system.filter"
	TagAuth      = "system.auth"
	TagTransport = "system.transport"
)

// tagTree lists every known tag, parents before children, in the order
// AllTags returns them.
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
	TagFilter,
	TagAuth,
	TagTransport,
}

// AllTags returns every known tag in tree order: parents before children,
// siblings stable, so anything that renders the whole taxonomy shows it
// the way the tree reads.
func AllTags() []string {
	out := make([]string, len(tagTree))
	copy(out, tagTree)
	return out
}

// ParentTag returns the enclosing tag path, or "" for the root and for a
// tag this package does not know: an unknown tag is inert rather than an
// error, because a peer may run a newer vocabulary.
func ParentTag(tag string) string {
	cut := strings.LastIndex(tag, ".")
	if cut < 0 {
		return ""
	}
	parent := tag[:cut]
	for _, known := range tagTree {
		if known == parent {
			return parent
		}
	}
	return ""
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

// BlockedBy returns the muted keys that hide a line carrying tags: a key
// matches when it is one of the tags or an ancestor of one. A caller hides
// the line when the result is non-empty, which is what makes muting a node
// mute its whole subtree.
//
// Keys that are not part of the taxonomy simply never match, so a caller can
// pass whatever gate names it holds. Results follow the order of tags, so the
// same input always yields the same output.
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
		for at := tag; at != ""; at = ParentTag(at) {
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
		// Walk root → leaf so ancestors land before their child.
		chain := []string{}
		for t := leaf; t != ""; t = ParentTag(t) {
			chain = append([]string{t}, chain...)
		}
		for _, t := range chain {
			add(t)
		}
	}
	return out
}
