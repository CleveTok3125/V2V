package main

// Message-entry chrome styles. Entries used to be assembled from hand-written
// SGR sequences; the chrome now goes through lipgloss so it follows the
// terminal colour profile like the tabs, input and footer. Colours stay ANSI
// basics where the old codes were basics, so the look is unchanged.

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// dimStyle greys secondary chrome: pending quotes, the collapse trailer, the
// local echo and placeholder bodies (ANSI 8, formerly "\x1b[90m").
var dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

// warnStyle marks a failed or unverified badge (ANSI 9, formerly "\x1b[91m").
var warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

// badgeStyle colours a verified badge with its palette colour.
func badgeStyle(badge string) lipgloss.Style {
	r, g, b := badgeRGB(badge)
	return lipgloss.NewStyle().Foreground(color.RGBA{R: r, G: g, B: b, A: 0xff})
}

// linkText wraps s in an OSC8 hyperlink. Built by hand rather than with
// lipgloss: lipgloss terminates the link with BEL, while the display
// sanitizer and the mention span regex expect the ST form ("\x1b\\").
func linkText(url, s string) string {
	return "\x1b]8;;" + url + "\x1b\\" + s + "\x1b]8;;\x1b\\"
}
