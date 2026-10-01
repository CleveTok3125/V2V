package main

import (
	"strings"

	"github.com/CleveTok3125/V2V/internal/wire"
)

// Notification gates: informational notices can be muted on screen with
// /notify or the ui.notify config, while still landing in the system tab
// so nothing is lost. Critical warnings (chain integrity, send guards,
// connection loss, command output) are never gated.
//
// Gates are tag paths, the same vocabulary the server publishes, so muting
// a node hides its whole subtree and a notice kind added later needs no new
// switch. The constants that spelled the kinds out are gone; use the tags
// from internal/wire.

// NotifyState is the set of muted tag paths, adjustable at runtime.
type NotifyState struct {
	Muted      map[string]bool
	PowMinTier int
}

// configMutedTags turns the loaded config's gates into a muted set, so the
// subtree rule applies to config exactly as it does at runtime.
func configMutedTags() map[string]bool {
	muted := map[string]bool{}
	for _, tag := range wire.AllTags() {
		if !ClientCfg.NotifyTag(tag) {
			muted[tag] = true
		}
	}
	return muted
}

// notifyStateFromConfig builds the runtime state from the loaded config: a
// false entry in ui.notify mutes that tag, and every other tag is shown.
func notifyStateFromConfig() NotifyState {
	return NotifyState{Muted: configMutedTags(), PowMinTier: ClientCfg.NotifyPowMinTier()}
}

// quietNames returns the --quiet names as a muted set, applying the same
// subtree rule as the config gates. The flag is parsed before either the
// dial or the gate flow, so it is the one gate both can see.
func quietNames() map[string]bool {
	muted := map[string]bool{}
	for _, name := range CLI.Quiet {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "all" {
			for _, tag := range wire.AllTags() {
				muted[tag] = true
			}
			continue
		}
		if wire.HasTag(wire.AllTags(), name) {
			muted[name] = true
		}
	}
	return muted
}

// notifyPowWanted reports whether the "solving PoW" notice for a tier may
// print, for code that runs before there is a session to hold the runtime
// gates — the pre-connect join gate. It reads the config and the --quiet
// names, and applies the same subtree rule as the runtime gates, so muting
// the root silences it too. Without --quiet here the same switch would mute
// the in-chat notice and leave this one, depending on when it fired.
func notifyPowWanted(tier int) bool {
	muted := quietNames()
	if ClientCfg != nil {
		for tag, on := range configMutedTags() {
			muted[tag] = on
		}
	}
	return len(wire.BlockedBy(wire.WithTags(wire.TagPow), muted)) == 0 &&
		(ClientCfg == nil || tier >= ClientCfg.NotifyPowMinTier())
}

// notifyTagsAllowed reports whether a line carrying tags may print live.
// A line is hidden when any tag it carries, or any ancestor of one, is
// muted, so switching off a node hides its whole subtree. A line with no
// tags is always live.
func (s *Session) notifyTagsAllowed(tags []string) bool {
	// Chat carries no tags and is never gated. Short-circuiting here keeps
	// the render path off the lock and off a per-call map.
	if len(tags) == 0 {
		return true
	}
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return s.notifyTagsAllowedLocked(tags)
}

func (s *Session) notifyTagsAllowedLocked(tags []string) bool {
	return len(wire.BlockedBy(tags, s.Display.Notify.Muted)) == 0
}

// notifyPowLive reports whether the "solving PoW" notice for a tier
// prints live: it is gated both by the pow subtree and the tier floor. The
// subtree test has to walk ancestors like every other gate — a direct lookup
// of the system.pow key would let a muted root (or --quiet system) leave
// this one line printing.
func (s *Session) notifyPowLive(tier int) bool {
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return len(wire.BlockedBy(wire.WithTags(wire.TagPow), s.Display.Notify.Muted)) == 0 &&
		tier >= s.Display.Notify.PowMinTier
}

// notifyTagsForWire returns the tags a wire is gated on, or nil for content
// that is always live. Only system lines are notices; chat is never muted.
// An untagged system line falls back to the root, so a notice from a peer
// that predates tagging is still covered by the system switch.
func notifyTagsForWire(msg WireMessage) []string {
	if msg.Type != "system" {
		return nil
	}
	if len(msg.Tags) == 0 {
		return wire.WithTags(wire.TagRoot)
	}
	return msg.Tags
}

// notifyPowMinTier returns the lowest PoW tier announced live.
func (s *Session) notifyPowMinTier() int {
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return s.Display.Notify.PowMinTier
}

// notifySetTag mutes or unmutes one tag path. It reports false for a path
// that is not in the taxonomy, so /notify cannot create a gate that no
// notice would ever match. The caller emits the confirmation after
// releasing NotifyMu, so the lock order stays leaf-only.
func (s *Session) notifySetTag(tag string, on bool) bool {
	if !wire.HasTag(wire.AllTags(), tag) {
		return false
	}
	s.Display.NotifyMu.Lock()
	defer s.Display.NotifyMu.Unlock()
	if s.Display.Notify.Muted == nil {
		s.Display.Notify.Muted = map[string]bool{}
	}
	if on {
		delete(s.Display.Notify.Muted, tag)
	} else {
		s.Display.Notify.Muted[tag] = true
	}
	return true
}

// notifySetAll flips every gate at once.
func (s *Session) notifySetAll(on bool) {
	s.Display.NotifyMu.Lock()
	if on {
		s.Display.Notify.Muted = map[string]bool{}
	} else {
		muted := make(map[string]bool, len(wire.AllTags()))
		for _, tag := range wire.AllTags() {
			muted[tag] = true
		}
		s.Display.Notify.Muted = muted
	}
	s.Display.NotifyMu.Unlock()
}

// notifyTagEnabled reports whether one tag path is currently shown. A
// path is shown unless it is muted itself, and muting a node covers its
// subtree — so a child of a muted parent reads as off even though its own
// key is not set.
func (s *Session) notifyTagEnabled(tag string) bool {
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return len(wire.BlockedBy(wire.WithTags(tag), s.Display.Notify.Muted)) == 0
}

// notifySetPowMinTier sets the lowest PoW tier announced live.
func (s *Session) notifySetPowMinTier(tier int) {
	s.Display.NotifyMu.Lock()
	s.Display.Notify.PowMinTier = tier
	s.Display.NotifyMu.Unlock()
}

// applyQuietFlags applies the repeatable --quiet names on top of the config
// gates. The names are tag paths, and "all" mutes the whole taxonomy. An
// unknown name is ignored, so a --quiet written for a newer build does not
// stop the ones that are understood.
func (s *Session) applyQuietFlags() {
	for _, name := range CLI.Quiet {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "all" {
			s.notifySetAll(false)
			continue
		}
		s.notifySetTag(name, false)
	}
}

// parseOnOff accepts the on/off spellings shared with /meta.
func parseOnOff(v string) (bool, bool) {
	switch strings.ToLower(v) {
	case "on", "true":
		return true, true
	case "off", "false":
		return false, true
	default:
		return false, false
	}
}

func onOffLabel(on bool) string {
	if on {
		return "BẬT"
	}
	return "TẮT"
}
