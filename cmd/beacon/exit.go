package main

import (
	"bufio"
	"fmt"
	"os"
)

// exit ends Beacon with code. When Beacon has its own console window (started by double-clicking
// it on Windows), it first waits for Enter, so what it printed can still be read.
func exit(code int) {
	waitIfOwnConsole()
	os.Exit(code)
}

func waitIfOwnConsole() {
	if !ownConsole() {
		return
	}
	fmt.Print("\nPress Enter to close this window.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
