//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var getConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// ownConsole reports whether Beacon has a console window of its own, as when it is started by
// double-clicking it: then the window closes the moment Beacon exits.
func ownConsole() bool {
	ids := make([]uint32, 2)
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return n == 1
}
