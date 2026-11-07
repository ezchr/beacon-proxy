package bridge

import (
	"os"
	"strings"
	"sync/atomic"
)

// ClosedFile holds the message players get while the network is closed: closing writes it, opening
// removes it, so a restart stays closed. Set by the program (default "closed.txt").
var ClosedFile = "closed.txt"

var closedMessage atomic.Pointer[string]

// LoadClosed reads ClosedFile at start.
func LoadClosed() {
	if b, err := os.ReadFile(ClosedFile); err == nil {
		msg := strings.TrimSpace(string(b))
		closedMessage.Store(&msg)
	}
}

// Closed reports whether the network is closed, and the message players get.
func Closed() (string, bool) {
	if p := closedMessage.Load(); p != nil {
		return *p, true
	}
	return "", false
}

// Close closes the network: everyone but the admins is disconnected with msg, and new joins
// (Friends tab or address) are turned away with it until Open. It returns how many were disconnected.
func (n *Network) Close(msg string) (int, error) {
	if err := os.WriteFile(ClosedFile, []byte(msg+"\n"), 0o644); err != nil {
		return 0, err
	}
	closedMessage.Store(&msg)
	kicked := 0
	n.sessions.Range(func(_, v any) bool {
		s := v.(*switchSession)
		if !n.IsAdmin(s.id.XUID, s.id.DisplayName) {
			s.disconnect(msg)
			kicked++
		}
		return true
	})
	n.Log.Info("network closed", "message", msg, "disconnected", kicked)
	return kicked, nil
}

// Open lets everyone join again.
func (n *Network) Open() error {
	if err := os.Remove(ClosedFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	closedMessage.Store(nil)
	n.Log.Info("network open")
	return nil
}
