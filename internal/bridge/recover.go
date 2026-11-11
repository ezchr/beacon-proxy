package bridge

import (
	"fmt"
	"runtime/debug"
)

// recoverSession ends only this player's session when one of its goroutines panics, instead of
// the whole relay. Use it as the first defer of every goroutine that works for a session.
func (s *switchSession) recoverSession(where string) {
	r := recover()
	if r == nil {
		return
	}
	if s.log != nil {
		s.log.Error("network: session goroutine panicked, ending this session", "where", where, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	}
	if s.cancel != nil {
		s.cancel(fmt.Errorf("internal error in %s: %v", where, r))
	}
}
