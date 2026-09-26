package main

import (
	"io"
	"testing"
)

// Repro for the echo-mismatch warning burst: during heavy continuous
// chat, "Echo không khớp tin đang chờ" lines appear in one big batch
// (up to the 16-entry stash cap), then stop. Rejoining clears it.
// Mechanics: a live echo that never matches a placeholder gets stashed
// and later reaped as "stale" in one batch. The fix keeps the stash
// only for genuinely unseen IDs: duplicates of consumed messages drop,
// foreign IDs still stash and warn.
func stashTestWire(name string, tmpID uint64) WireMessage {
	return WireMessage{Type: "chat", Time: "12:00", DisplayName: name, Text: "x", TmpID: tmpID}
}

func stashTestWireReply(name string, tmpID, replyTo uint64) WireMessage {
	return WireMessage{Type: "chat", Time: "12:00", DisplayName: name, Text: "x", TmpID: tmpID, ReplyTo: replyTo}
}

func stashTestSession(t *testing.T) *Session {
	t.Helper()
	sess := NewSession()
	sess.Display.Out = io.Discard
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Username = "A#h"
	return sess
}

func stashTestConsume(sess *Session, wire WireMessage, allowStash bool) {
	sess.Display.DisplayMu.Lock()
	sess.verifyReplayWire(wire, allowStash)
	sess.Display.DisplayMu.Unlock()
}

// A duplicate of an already-consumed echo must drop, never stash.
func TestEchoDuplicateDropped(t *testing.T) {
	sess := stashTestSession(t)
	sess.Pending.PendingPlaceholders = []pendingMsg{{tmpID: 9, replyTo: 0}}
	stashTestConsume(sess, stashTestWire("A#h", 9), true)
	if len(sess.Pending.PendingPlaceholders) != 0 {
		t.Fatal("first echo must consume the placeholder")
	}
	// Same tmp_id, different reply_to: still a duplicate of a consumed
	// message, must drop.
	stashTestConsume(sess, stashTestWireReply("A#h", 9, 5), true)
	for i := uint64(0); i < 20; i++ {
		stashTestConsume(sess, stashTestWire("A#h", 9), true)
	}
	if got := len(sess.Pending.PendingEchoes); got != 0 {
		t.Fatalf("duplicate echoes must not stash, got %d", got)
	}
	if _, stale := reapStaleEchoes(sess.Pending.PendingEchoes, 0); len(stale) != 0 {
		t.Fatalf("nothing stashed, nothing stale: got %d", len(stale))
	}
}

// A genuinely unseen ID with placeholders present still stashes and
// warns on staleness (server-rewrite signal).
func TestEchoForeignStashed(t *testing.T) {
	sess := stashTestSession(t)
	sess.Pending.PendingPlaceholders = []pendingMsg{{tmpID: 1, replyTo: 0}}
	stashTestConsume(sess, stashTestWire("A#h", 1000), true)
	if got := len(sess.Pending.PendingEchoes); got != 1 {
		t.Fatalf("foreign echo must stash once, got %d", got)
	}
	if _, stale := reapStaleEchoes(sess.Pending.PendingEchoes, 0); len(stale) != 1 {
		t.Fatalf("foreign echo must warn once on staleness, got %d", len(stale))
	}
}

func TestEchoNoStashDuringSync(t *testing.T) {
	sess := stashTestSession(t)
	sess.Chain.InSync = true
	stashTestConsume(sess, stashTestWire("A#h", 5), true)
	if got := len(sess.Pending.PendingEchoes); got != 0 {
		t.Fatalf("replay must not stash, got %d", got)
	}
}

func TestEchoRaceStillStashed(t *testing.T) {
	sess := stashTestSession(t)
	stashTestConsume(sess, stashTestWire("A#h", 5), true)
	if got := len(sess.Pending.PendingEchoes); got != 1 {
		t.Fatalf("live pre-placeholder race must stash once, got %d", got)
	}
}

// The race path (send.go takeStashedEcho) records the ID, so a later
// duplicate of the same echo drops.
func TestEchoRaceRecordsSeen(t *testing.T) {
	sess := stashTestSession(t)
	stashTestConsume(sess, stashTestWire("A#h", 7), true)
	if got := len(sess.Pending.PendingEchoes); got != 1 {
		t.Fatalf("race must stash once, got %d", got)
	}
	// Simulate send.go placeholder tracking for the same message.
	var have bool
	sess.Pending.PendingEchoes, _, have = takeStashedEcho(sess.Pending.PendingEchoes, 7)
	if !have {
		t.Fatal("takeStashedEcho must find the raced echo")
	}
	sess.Pending.SeenTmpIDs = noteConsumedTmpID(sess.Pending.SeenTmpIDs, 7)
	stashTestConsume(sess, stashTestWire("A#h", 7), true)
	if got := len(sess.Pending.PendingEchoes); got != 0 {
		t.Fatalf("post-race duplicate must drop, got %d", got)
	}
}

// tmp_id 0 echoes never stash, even on mismatch.
func TestEchoZeroTmpIDNeverStashed(t *testing.T) {
	sess := stashTestSession(t)
	sess.Pending.PendingPlaceholders = []pendingMsg{{tmpID: 1, replyTo: 0, text: "other"}}
	stashTestConsume(sess, stashTestWire("A#h", 0), true)
	if got := len(sess.Pending.PendingEchoes); got != 0 {
		t.Fatalf("tmp_id 0 mismatch must not stash, got %d", got)
	}
}

func TestSeenTmpIDsBounded(t *testing.T) {
	var seen []uint64
	for i := uint64(1); i <= maxSeenTmpIDs+6; i++ {
		seen = noteConsumedTmpID(seen, i)
	}
	if len(seen) != maxSeenTmpIDs {
		t.Fatalf("seen set must stay bounded at %d, got %d", maxSeenTmpIDs, len(seen))
	}
	if seenTmpID(seen, 1) {
		t.Fatal("oldest ID must be evicted")
	}
	if !seenTmpID(seen, maxSeenTmpIDs+6) {
		t.Fatal("newest ID must be recorded")
	}
	// Re-recording a known ID must not grow the set.
	seen = noteConsumedTmpID(seen, maxSeenTmpIDs+6)
	if len(seen) != maxSeenTmpIDs {
		t.Fatalf("duplicate record must not grow the set, got %d", len(seen))
	}
}
