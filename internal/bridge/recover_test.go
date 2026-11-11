package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A panic in one session's goroutine ends that session only, with the reason as its cause.
func TestRecoverSessionEndsOnlyThatSession(t *testing.T) {
	newSession := func() *switchSession {
		ctx, cancel := context.WithCancelCause(context.Background())
		return &switchSession{ctx: ctx, cancel: cancel, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	}
	a, b := newSession(), newSession()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer a.recoverSession("test")
		var m map[string]int
		m["x"]++ // nil map write
	}()
	<-done
	select {
	case <-a.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the session that panicked is still running")
	}
	if cause := context.Cause(a.ctx); cause == nil || !strings.Contains(cause.Error(), "internal error in test") {
		t.Errorf("cause = %v", cause)
	}
	if b.ctx.Err() != nil {
		t.Error("the other session ended too")
	}
	// No panic: nothing ends.
	func() { defer b.recoverSession("test") }()
	if b.ctx.Err() != nil || errors.Is(context.Cause(b.ctx), context.Canceled) {
		t.Error("a goroutine that returned normally ended its session")
	}
}

// The sub-chunk timer recovers too: a session without a client panics on its write and ends.
func TestAirPendingPanicEndsSession(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	s := &switchSession{ctx: ctx, cancel: cancel, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.subPending = map[[2]int32][]pendingSub{{0, 0}: {{due: time.Now().Add(-time.Second)}}}
	s.airPending() // sendClient on a nil client panics
	if ctx.Err() == nil {
		t.Fatal("session still running after its timer panicked")
	}
}
