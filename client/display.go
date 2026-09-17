package main

import (
	"fmt"
)

// Session display funnel (moved from main): tab buffers plus the single
// local-output path. Callers hold DisplayMu, except switchTab which
// takes it.
func (s *Session) emitTab(tab int, line string) {
	if tab == TabChat {
		s.Display.TabChat.append(line)
	} else {
		s.Display.TabSys.append(line)
	}
	// Tab 1 shows the full legacy stream, so it is unaffected by tabs.
	// Tab 2 is purely additive and shows only its own lines.
	if tab == s.Display.ActiveTab || s.Display.ActiveTab == TabChat {
		fmt.Fprint(s.Display.Out, line)
		s.Display.PrintGen++
	}
}

// emitLocalFeedback is the single funnel for all local output: it
// buffers the line in TabSystem and prints it on the active tab
// immediately, so commands always respond visibly while staying
// reviewable in Tab 2. Every current and future local message must
// go through here — printing to out directly bypasses Tab 2 and
// leaves it incomplete. Caller must hold DisplayMu.
func (s *Session) emitLocalFeedback(line string) {
	s.Display.TabSys.append(line)
	fmt.Fprint(s.Display.Out, line)
	s.Display.PrintGen++
}
