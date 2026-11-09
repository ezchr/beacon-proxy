package bridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func TestServerPaletteIsShared(t *testing.T) {
	n := &Network{}
	union, err := newBlockPalette(nil)
	if err != nil {
		t.Fatal(err)
	}
	blocks := []protocol.BlockEntry{{Name: "addon:ruby", Properties: map[string]any{}}}
	a, am, err := n.serverPalette(blocks, union, false)
	if err != nil {
		t.Fatal(err)
	}
	b, bm, _ := n.serverPalette([]protocol.BlockEntry{{Name: "addon:ruby", Properties: map[string]any{}}}, union, false)
	if a != b || am != bm {
		t.Fatal("the same block list must get the same palette and map")
	}
	if c, _, _ := n.serverPalette([]protocol.BlockEntry{{Name: "addon:jade", Properties: map[string]any{}}}, union, false); c == a {
		t.Fatal("another block list must get its own palette")
	}
	if am == nil {
		t.Fatal("a custom block makes the palettes differ, so there must be a map")
	}
}

func TestUnchangedSkipsTheDiskOnceKnown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "items.bin")
	payload := []byte{1, 2, 3}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	n := &Network{}
	if !n.unchanged("s/items", path, packet.IDItemRegistry, payload) {
		t.Fatal("the cached bytes must count as unchanged")
	}
	_ = os.Remove(path)
	if !n.unchanged("s/items", path, packet.IDItemRegistry, payload) {
		t.Fatal("once known, the same bytes must not need the file")
	}
}
