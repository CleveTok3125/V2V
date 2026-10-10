//go:build windows

package main

// adoptDirOwner is a no-op on Windows: the operator and the service run as
// the same user, so a replacement never changes hands.
func adoptDirOwner(string) error { return nil }
