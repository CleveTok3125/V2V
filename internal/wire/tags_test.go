package wire

import "testing"

// TestWithTagsImpliesAncestors: a producer names the leaf and the wire
// carries the whole chain, root first. This is what lets the client mute
// a parent without knowing which children exist.
func TestWithTagsImpliesAncestors(t *testing.T) {
	got := WithTags(TagPowScreen)
	want := []string{"system", "system.pow", "system.pow.screening"}
	if len(got) != len(want) {
		t.Fatalf("WithTags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WithTags = %v, want %v", got, want)
		}
	}
}

func TestWithTagsMergesSeveralLeaves(t *testing.T) {
	got := WithTags(TagHistoryRecover, TagPowScreen)
	for _, want := range []string{"system", "system.history", "system.history.recover", "system.pow", "system.pow.screening"} {
		if !HasTag(got, want) {
			t.Errorf("WithTags(%s,%s) = %v, missing %q", TagHistoryRecover, TagPowScreen, got, want)
		}
	}
	// Root stays first and appears once.
	if got[0] != TagRoot {
		t.Errorf("root must come first, got %v", got)
	}
	seenRoot := 0
	for _, tag := range got {
		if tag == TagRoot {
			seenRoot++
		}
	}
	if seenRoot != 1 {
		t.Errorf("root must appear exactly once, got %v", got)
	}
}

// TestWithTagsStable: tagging the same line twice must not duplicate or
// reorder, so a producer can be composed without surprises.
func TestWithTagsStable(t *testing.T) {
	once := WithTags(TagHistoryRecover)
	twice := WithTags(TagHistoryRecover, TagHistoryRecover)
	if len(once) != len(twice) {
		t.Fatalf("repeated leaf changed the result: %v vs %v", once, twice)
	}
}

func TestParentTag(t *testing.T) {
	cases := []struct{ tag, want string }{
		{TagRoot, ""},
		{TagPow, TagRoot},
		{TagPowScreen, TagPow},
		{TagHistoryEnd, TagHistory},
		{"system.pow.unknownleaf", TagPow},
		{"bogus", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ParentTag(tc.tag); got != tc.want {
			t.Errorf("ParentTag(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

// TestAllTagsListsParentsBeforeChildren: anything rendering the taxonomy
// shows this order, so a child must never precede its parent.
func TestAllTagsListsParentsBeforeChildren(t *testing.T) {
	position := map[string]int{}
	for i, tag := range AllTags() {
		if _, dup := position[tag]; dup {
			t.Fatalf("duplicate tag %q in AllTags", tag)
		}
		position[tag] = i
	}
	for _, tag := range AllTags() {
		parent := ParentTag(tag)
		if parent == "" {
			continue
		}
		at, known := position[parent]
		if !known {
			t.Errorf("tag %q has parent %q which AllTags does not list", tag, parent)
			continue
		}
		if at > position[tag] {
			t.Errorf("parent %q (pos %d) must precede child %q (pos %d)", parent, at, tag, position[tag])
		}
	}
}

func TestHasTagAndHasAnyTag(t *testing.T) {
	tags := WithTags(TagPowScreen)
	if !HasTag(tags, TagPowScreen) || !HasTag(tags, TagRoot) {
		t.Errorf("HasTag must find a carried tag and its ancestors: %v", tags)
	}
	if HasTag(tags, TagJoin) || HasTag(nil, TagRoot) {
		t.Error("HasTag must not find absent tags")
	}
	if !HasAnyTag(tags, TagJoin, TagPowScreen) {
		t.Error("HasAnyTag must match on the second candidate")
	}
	if HasAnyTag(tags, TagJoin, TagLeave) {
		t.Error("HasAnyTag must not match when no candidate is present")
	}
	if HasAnyTag(nil, TagRoot) {
		t.Error("HasAnyTag on no tags must be false")
	}
}

// TestWithTagsUnknownLeafIsCarriedVerbatim: a peer may publish a tag this
// build does not know. The line must still be filterable as a notice, and
// the tag must survive untouched so a client that does know it can act on
// it; only its ancestors are unknowable here, so the root is added.
func TestWithTagsUnknownLeafIsCarriedVerbatim(t *testing.T) {
	const unknown = "system.futuresubtype"
	got := WithTags(unknown)
	if !HasTag(got, TagRoot) {
		t.Fatalf("unknown leaf must still carry the root, got %v", got)
	}
	if !HasTag(got, unknown) {
		t.Fatalf("unknown leaf must be carried verbatim, got %v", got)
	}
}

// TestDeclaredTagsAreInTheTree: a tag constant that never reaches tagTree
// compiles fine and satisfies every other test here, but is invisible to
// anything that walks the taxonomy, so a mute built on the tree could not
// switch it off. This pairs the two lists so a constant added on one side
// only fails here.
func TestDeclaredTagsAreInTheTree(t *testing.T) {
	declared := map[string]bool{}
	for _, tag := range AllTags() {
		declared[tag] = true
	}
	// Tags the server publishes, paired with the constant that names them.
	produced := map[string]string{
		"join": TagJoin, "leave": TagLeave, "date": TagDate,
		"audit":           TagAudit,
		"history":         TagHistory,
		"history begin":   TagHistoryBegin,
		"history older":   TagHistoryOlder,
		"history recover": TagHistoryRecover,
		"history end":     TagHistoryEnd,
		"history used up": TagHistoryExhausted,
		"pow":             TagPow,
		"gate":            TagPowGate,
		"screening":       TagPowScreen,
		"limit":           TagLimit, "envelope": TagEnvelope, "trip": TagTrip,
		"filter": TagFilter, "auth": TagAuth, "transport": TagTransport,
	}
	for kind, tag := range produced {
		if !declared[tag] {
			t.Errorf("notice kind %q uses %q, which AllTags does not list", kind, tag)
		}
	}
}

// TestBlockedByMatchesAncestors: muting a node has to hide everything
// below it, which only works if a line is tested against its ancestors and
// not just the tags it names.
func TestBlockedByMatchesAncestors(t *testing.T) {
	cases := []struct {
		name    string
		tags    []string
		muted   []string
		wantHit string
	}{
		{"nothing muted", WithTags(TagPowScreen), nil, ""},
		{"root muted hides a notice", WithTags(TagPowScreen), []string{TagRoot}, TagRoot},
		{"intermediate muted hides child", WithTags(TagPowScreen), []string{TagPow}, TagPow},
		{"own tag muted", WithTags(TagPowScreen), []string{TagPowScreen}, TagPowScreen},
		{"unrelated tag does not block", WithTags(TagPowScreen), []string{TagJoin, TagDate}, ""},
		{"sibling subtree does not block", WithTags(TagHistoryEnd), []string{TagPow}, ""},
		{"one of several tags muted", WithTags(TagHistoryRecover, TagPowScreen), []string{TagPowScreen}, TagPowScreen},
		{"muted ancestor of one of several", WithTags(TagHistoryRecover, TagPowScreen), []string{TagHistory}, TagHistory},
		{"untagged line is never blocked", nil, []string{TagRoot}, ""},
	}
	for _, tc := range cases {
		muted := map[string]bool{}
		for _, m := range tc.muted {
			muted[m] = true
		}
		blocked := BlockedBy(tc.tags, muted)
		if tc.wantHit == "" {
			if len(blocked) != 0 {
				t.Errorf("%s: BlockedBy(%v, %v) = %v, want none", tc.name, tc.tags, tc.muted, blocked)
			}
			continue
		}
		if len(blocked) != 1 || blocked[0] != tc.wantHit {
			t.Errorf("%s: BlockedBy(%v, %v) = %v, want [%s]", tc.name, tc.tags, tc.muted, blocked, tc.wantHit)
		}
	}
}

// TestBlockedByIsDeterministic: the result feeds a user-facing "muted by"
// message, so the same inputs must not produce a different order run to run.
func TestBlockedByIsDeterministic(t *testing.T) {
	muted := map[string]bool{TagRoot: true, TagPow: true, TagPowScreen: true}
	tags := WithTags(TagPowScreen, TagJoin)
	want := []string{TagRoot, TagPow, TagPowScreen}
	for i := 0; i < 50; i++ {
		got := BlockedBy(tags, muted)
		if len(got) != len(want) {
			t.Fatalf("BlockedBy = %v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("BlockedBy = %v, want %v", got, want)
			}
		}
	}
}

// TestBlockedByUnknownTagStopsAtKnownAncestor: a peer may publish a tag
// this build does not know. Its ancestors below the known part still
// apply, and the walk must terminate at the root.
func TestBlockedByUnknownTagStopsAtKnownAncestor(t *testing.T) {
	muted := map[string]bool{TagRoot: true}
	blocked := BlockedBy([]string{TagRoot, "system.pow.future"}, muted)
	if len(blocked) != 1 || blocked[0] != TagRoot {
		t.Fatalf("BlockedBy = %v, want [%s]", blocked, TagRoot)
	}
}

// TestBlockedByAlwaysTestsTheRoot: a peer can publish a chain that stops
// below anything this build knows. Muting the root has to still hide it,
// otherwise a partial chain is a way to stay visible while muted.
func TestBlockedByAlwaysTestsTheRoot(t *testing.T) {
	for _, chain := range [][]string{
		{"system.pow.screening"},
		{"system.future.leaf"},
		{"system"},
	} {
		blocked := BlockedBy(chain, map[string]bool{TagRoot: true})
		if len(blocked) != 1 || blocked[0] != TagRoot {
			t.Errorf("BlockedBy(%v, root muted) = %v, want [%s]", chain, blocked, TagRoot)
		}
	}
}

// TestWithTagsKeepsKnownAncestorsOfUnknownLeaf: a peer on a newer vocabulary
// can publish a leaf below a level this build does not know. The known
// ancestors of that leaf must still travel, otherwise the line detaches
// from the tree and no gate on its real ancestors can ever hide it.
func TestWithTagsKeepsKnownAncestorsOfUnknownLeaf(t *testing.T) {
	got := WithTags("system.pow.unknown.deep")
	for _, want := range []string{TagRoot, TagPow, "system.pow.unknown.deep"} {
		if !HasTag(got, want) {
			t.Errorf("WithTags = %v, want it to carry %q", got, want)
		}
	}
	if got[0] != TagRoot {
		t.Errorf("root must come first, got %v", got)
	}
	if HasTag(got, "system.pow.unknown") {
		t.Errorf("an unknown level must not be invented, got %v", got)
	}
}

// TestBlockedByReachesAncestorsPastUnknownLevels: the same peer chain must
// still be hidden by a mute on a known ancestor, which is the whole point
// of carrying the chain rather than the leaf.
func TestBlockedByReachesAncestorsPastUnknownLevels(t *testing.T) {
	leaf := WithTags("system.pow.unknown.deep")
	for _, mutedKey := range []string{TagRoot, TagPow} {
		blocked := BlockedBy(leaf, map[string]bool{mutedKey: true})
		if len(blocked) != 1 || blocked[0] != mutedKey {
			t.Errorf("BlockedBy(%v, %q muted) = %v, want [%s]", leaf, mutedKey, blocked, mutedKey)
		}
	}
	// A mute on an unrelated branch must still leave it visible.
	if blocked := BlockedBy(leaf, map[string]bool{TagHistory: true}); len(blocked) != 0 {
		t.Errorf("BlockedBy = %v, want none", blocked)
	}
}

// TestOffTreeTagStaysOffTree: a chain that never reaches a known level is
// carried as given and only the root applies, rather than being attached
// to some branch it does not belong to.
func TestOffTreeTagStaysOffTree(t *testing.T) {
	got := WithTags("elsewhere.thing")
	if !HasTag(got, TagRoot) || !HasTag(got, "elsewhere.thing") {
		t.Fatalf("WithTags = %v, want root plus the tag as given", got)
	}
	if len(got) != 2 {
		t.Fatalf("WithTags invented a chain for an off-tree tag: %v", got)
	}
}
