package main

import (
	"strings"

	"github.com/CleveTok3125/V2V/internal/wire"
)

// Notification gates: informational notices can be muted on screen with
// /notify or the ui.notify.* config, while still landing in the system
// tab so nothing is lost. Critical warnings (chain integrity, send
// guards, connection loss, command output) are never gated.
const (
	NotifyKindPow     = "pow"
	NotifyKindHistory = "history"
	NotifyKindJoin    = "join"
	NotifyKindDate    = "date"
	NotifyKindSystem  = "system"
)

// NotifyState mirrors the config gates and is adjustable at runtime.
type NotifyState struct {
	Pow        bool
	PowMinTier int
	History    bool
	Join       bool
	Date       bool
	System     bool
}

// notifyStateFromConfig builds the runtime state from the loaded config.
func notifyStateFromConfig() NotifyState {
	return NotifyState{
		Pow:        ClientCfg.NotifyPow(),
		PowMinTier: ClientCfg.NotifyPowMinTier(),
		History:    ClientCfg.NotifyHistory(),
		Join:       ClientCfg.NotifyJoin(),
		Date:       ClientCfg.NotifyDate(),
		System:     ClientCfg.NotifySystem(),
	}
}

// notifyMutedTagsLocked renders the current gates as tag keys, so gating
// reads the same vocabulary the server publishes. The root key covers every
// notice, which is what makes the system switch a mute-everything control.
func (s *Session) notifyMutedTagsLocked() map[string]bool {
	n := s.Display.Notify
	return map[string]bool{
		wire.TagRoot:    !n.System,
		wire.TagJoin:    !n.Join,
		wire.TagLeave:   !n.Join,
		wire.TagDate:    !n.Date,
		wire.TagPow:     !n.Pow,
		wire.TagHistory: !n.History,
	}
}

// notifyTagsAllowed reports whether a line carrying tags may print live.
// A line is hidden when any tag it carries, or any ancestor of one, is
// muted, so switching off a node hides its whole subtree. A line with no
// tags is always live.
func (s *Session) notifyTagsAllowed(tags []string) bool {
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return s.notifyTagsAllowedLocked(tags)
}

func (s *Session) notifyTagsAllowedLocked(tags []string) bool {
	return len(wire.BlockedBy(tags, s.notifyMutedTagsLocked())) == 0
}

// notifyPowLive reports whether the "solving PoW" notice for a tier
// prints live: it is gated both by the pow switch and the tier floor.
func (s *Session) notifyPowLive(tier int) bool {
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return s.Display.Notify.Pow && tier >= s.Display.Notify.PowMinTier
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

// notifyKindNames lists the toggleable kinds in help order.
var notifyKindNames = []string{NotifyKindPow, NotifyKindHistory, NotifyKindJoin, NotifyKindDate, NotifyKindSystem}

// notifySnapshot returns a copy of the current gates.
func (s *Session) notifySnapshot() NotifyState {
	s.Display.NotifyMu.RLock()
	defer s.Display.NotifyMu.RUnlock()
	return s.Display.Notify
}

// notifySetKind flips one kind's gate. It reports false for an unknown
// kind. The caller emits the confirmation after releasing NotifyMu, so
// the lock order stays leaf-only.
func (s *Session) notifySetKind(kind string, on bool) bool {
	s.Display.NotifyMu.Lock()
	defer s.Display.NotifyMu.Unlock()
	switch kind {
	case NotifyKindPow:
		s.Display.Notify.Pow = on
	case NotifyKindHistory:
		s.Display.Notify.History = on
	case NotifyKindJoin:
		s.Display.Notify.Join = on
	case NotifyKindDate:
		s.Display.Notify.Date = on
	case NotifyKindSystem:
		s.Display.Notify.System = on
	default:
		return false
	}
	return true
}

// notifySetAll flips every gate at once.
func (s *Session) notifySetAll(on bool) {
	s.Display.NotifyMu.Lock()
	s.Display.Notify.Pow = on
	s.Display.Notify.History = on
	s.Display.Notify.Join = on
	s.Display.Notify.Date = on
	s.Display.Notify.System = on
	s.Display.NotifyMu.Unlock()
}

// notifySetPowMinTier sets the lowest PoW tier announced live.
func (s *Session) notifySetPowMinTier(tier int) {
	s.Display.NotifyMu.Lock()
	s.Display.Notify.PowMinTier = tier
	s.Display.NotifyMu.Unlock()
}

// applyQuietFlags applies the repeatable --quiet names on top of the
// config gates. Unknown names are ignored.
func (s *Session) applyQuietFlags() {
	for _, name := range CLI.Quiet {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "all" {
			s.notifySetAll(false)
			continue
		}
		s.notifySetKind(name, false)
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
