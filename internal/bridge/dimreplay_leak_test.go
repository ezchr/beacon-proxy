package bridge

import (
	"fmt"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// TestSettleKeepsNoChangesAfterWindow is the leak of 2026-11-10: block changes around a kept spawn
// were stored for the whole connection (2.6 GB on a PvP arena next to the spawn).
func TestSettleKeepsNoChangesAfterWindow(t *testing.T) {
	st := &chunkSettle{
		chunks:   map[[2]int32]*keptChunk{{0, 0}: {chunk: []byte{1}}},
		far:      map[[2]int32]*keptChunk{},
		until:    time.Now().Add(time.Hour),
		farUntil: time.Now().Add(time.Hour),
	}
	// Inside the window: changes are kept, but never more than maxAfter per chunk.
	for i := 0; i < maxAfter*3; i++ {
		st.changed(protocol.BlockPos{1, 64, 1}, []byte(fmt.Sprint(i)))
	}
	if got := len(st.chunks[[2]int32{0, 0}].after); got != maxAfter {
		t.Fatalf("kept %d changes inside the window, want the cap %d", got, maxAfter)
	}
	// After the settle window: nothing more is kept.
	st.chunks[[2]int32{0, 0}].after = nil
	st.until = time.Now().Add(-time.Second)
	for i := 0; i < 100; i++ {
		st.changed(protocol.BlockPos{1, 64, 1}, []byte(fmt.Sprint(i)))
	}
	if got := len(st.chunks[[2]int32{0, 0}].after); got != 0 {
		t.Fatalf("kept %d changes after the settle window, want 0", got)
	}
	// After the far window: the kept chunks are dropped.
	st.farUntil = time.Now().Add(-time.Second)
	if !st.expire() || st.chunks != nil || st.far != nil {
		t.Fatal("kept chunks not dropped after the far window")
	}
}
