//go:build !windows

package main

// ownConsole reports whether Beacon has a console window of its own (Windows only).
func ownConsole() bool { return false }
