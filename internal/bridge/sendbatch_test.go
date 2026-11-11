package bridge

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// runBatched never runs more than batch moves at once, and runs them all.
func TestRunBatched(t *testing.T) {
	var running, most, ran atomic.Int32
	moves := make([]func(), 20)
	for i := range moves {
		moves[i] = func() {
			n := running.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			running.Add(-1)
			ran.Add(1)
		}
	}
	runBatched(moves, 3)
	if ran.Load() != 20 {
		t.Fatalf("%d moves ran, want 20", ran.Load())
	}
	if most.Load() > 3 {
		t.Fatalf("%d moves at once, want at most 3", most.Load())
	}
}

// Players needing the same server palette at once share one build.
func TestServerPaletteBuiltOnce(t *testing.T) {
	var builds atomic.Int32
	release := make(chan struct{})
	paletteBuildHook = func() {
		builds.Add(1)
		<-release
	}
	defer func() { paletteBuildHook = nil }()
	n := &Network{}
	blocks := []protocol.BlockEntry{{Name: "minecraft:air"}, {Name: "minecraft:stone"}}
	union, err := newBlockPalette(blocks)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]*blockPalette, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bp, _, err := n.serverPalette(blocks, union, false)
			if err != nil {
				t.Error(err)
			}
			results[i] = bp
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if b := builds.Load(); b != 1 {
		t.Fatalf("%d builds, want 1", b)
	}
	for _, bp := range results {
		if bp != results[0] {
			t.Fatal("players got different palettes")
		}
	}
	if len(n.paletteBuilds) != 0 {
		t.Fatal("finished build still listed")
	}
}
