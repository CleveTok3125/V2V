//go:build !js

package main

import tea "charm.land/bubbletea/v2"

// runTUI runs the app on the local terminal. Bubble Tea owns stdin/stdout
// and restores them on exit.
func runTUI(a *app) error {
	_, err := tea.NewProgram(a).Run()
	return err
}

// notifyQuit lets the platform hook do any cleanup when the client quits.
// On desktop the process exits naturally; nothing extra is needed.
func notifyQuit() {}
