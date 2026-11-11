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
