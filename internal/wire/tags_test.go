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
		"audit": TagAudit,
		"gate":  TagPowGate, "screening": TagPowScreen,
		"limit": TagLimit, "envelope": TagEnvelope, "trip": TagTrip,
		"filter": TagFilter, "auth": TagAuth, "transport": TagTransport,
	}
	for kind, tag := range produced {
		if !declared[tag] {
			t.Errorf("notice kind %q uses %q, which AllTags does not list", kind, tag)
		}
	}
}
