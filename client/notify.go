package main

import (
	"strings"

	"github.com/CleveTok3125/V2V/lib/clientcore"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// Notification gates: informational notices can mute on screen through
// /notify or ui.notify, while still landing in the system tab. Critical
// warnings (chain integrity, send guards, connection loss, command
// output) never gate.
//
// Gates are tag paths in the vocabulary the server publishes, so muting
// a node hides its subtree and a new notice kind needs no new switch.

// NotifyState is the core's gate state; the client keeps it inside Display
// and owns the lock.
type NotifyState = clientcore.Notices

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

// quietNames turns the --quiet names into a muted set, applying the same
// subtree rule as the config gates. The flag is parsed before either the
// dial or the gate flow, so it is the one gate both can see. A name outside
// the taxonomy is ignored, so a flag written for a newer build does not stop
// the ones this build understands.
func quietNames() map[string]bool {
	muted := map[string]bool{}
	for _, name := range quietTagNames() {
		if name == "" {
			// "all": the whole taxonomy, which the caller cannot express
			// as a set of named nodes.
			for _, tag := range wire.AllTags() {
				muted[tag] = true
			}
			continue
		}
		muted[name] = true
	}
	return muted
}

// quietTagNames normalises the --quiet names into tag paths, with "" standing
// for "all". A name outside the taxonomy is dropped. The pre-connect gate
// reads these before a session exists, so both callers go through here
// rather than each parsing the flag its own way.
func quietTagNames() []string {
	names := wire.AllTags()
	var out []string
	for _, raw := range CLI.Quiet {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "all" {
			return []string{""}
		}
		if wire.HasTag(names, name) {
			out = append(out, name)
		}
	}
	return out
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

// notifyTagsAllowed reports whether a tagged line may print live.
//
// A muted tag or ancestor hides the whole subtree; untagged lines stay live.
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
	return s.Display.Notify.TagsAllowed(tags)
}

func (s *Session) notifyPowLive(tier int) bool {
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return s.Display.Notify.PowLive(tier)
}

// notifyWireAllowed reports whether a system wire may print live.
//
// A muted tag or ancestor hides it; PoW notices also check the tier floor.
func (s *Session) notifyWireAllowed(msg WireMessage) bool {
	// Untagged content (chat) is never gated, and the gate state is a map:
	// reading it without the lock races /notify, which writes it.
	if len(clientcore.TagsForWire(msg)) == 0 {
		return true
	}
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return s.Display.Notify.WireAllowed(msg)
}

// notifyPowMinTier returns the lowest PoW tier announced live.
func (s *Session) notifyPowMinTier() int {
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return s.Display.Notify.PowMinTier
}

// notifySetTag mutes or unmutes one tag path.
//
// Reports false for paths outside the taxonomy.
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

// notifyTagEnabled reports whether one tag path shows.
//
// A child of a muted parent reads as off.
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
// gates, through the same parsing the pre-connect gate uses.
func (s *Session) applyQuietFlags() {
	for _, name := range quietTagNames() {
		if name == "" {
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
