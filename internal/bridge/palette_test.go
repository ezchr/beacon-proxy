package bridge

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// TestVanillaPaletteMatchesGeyser checks the embedded vanilla states and the sort against Geyser's
// own palette file, when it is around (BEACON_GEYSER_PALETTE).
func TestVanillaPaletteMatchesGeyser(t *testing.T) {
	path := env("GEYSER_PALETTE")
	if path == "" {
		t.Skip("BEACON_GEYSER_PALETTE not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Blocks []map[string]any `nbt:"blocks"`
	}
	if err := nbt.NewDecoderWithEncoding(zr, nbt.BigEndian).Decode(&m); err != nil {
		t.Fatal(err)
	}
	p, err := newBlockPalette(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Blocks) != len(p.states) {
		t.Fatalf("geyser has %d states, we have %d", len(m.Blocks), len(p.states))
	}
	bad := 0
	for i, b := range m.Blocks {
		name, _ := b["name"].(string)
		states, _ := b["states"].(map[string]any)
		want := blockState{Name: name, Properties: encodeProperties(states)}
		if p.states[i] != want {
			if bad < 5 {
				t.Errorf("state %d: geyser %v, we %v", i, want, p.states[i])
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%d states differ", bad)
	}
}

func TestCustomBlocksShiftPalette(t *testing.T) {
	vanilla, _ := newBlockPalette(nil)
	withOres, err := newBlockPalette([]protocol.BlockEntry{
		{Name: "oresplus:ruby_ore", Properties: map[string]any{}},
		{Name: "minecraft:red_shrub", Properties: map[string]any{}}, // vanilla data-driven: ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(withOres.states) != len(vanilla.states)+1 {
		t.Fatalf("got %d states, want %d", len(withOres.states), len(vanilla.states)+1)
	}
	m := newBlockMap(vanilla, withOres)
	for i, s := range vanilla.states {
		if got := withOres.states[m.forward(uint32(i))]; got != s {
			t.Fatalf("state %d maps to %v, want %v", i, got, s)
		}
		if m.reverse(m.forward(uint32(i))) != uint32(i) {
			t.Fatalf("state %d does not map back", i)
		}
	}
	ruby := withOres.index[blockState{Name: "oresplus:ruby_ore", Properties: encodeProperties(nil)}]
	if vanilla.states[m.reverse(ruby)].Name != "minecraft:air" {
		t.Fatalf("custom block maps back to %v, want air", vanilla.states[m.reverse(ruby)])
	}
}

func TestCustomBlockTraitsAddStates(t *testing.T) {
	pillar := protocol.BlockEntry{Name: "addon:pillar", Properties: map[string]any{
		"traits": []any{map[string]any{"name": "minecraft:placement_position",
			"enabled_states": map[string]any{"block_face": uint8(1), "vertical_half": uint8(0)}}},
	}}
	states, err := customBlockStates(pillar)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 6 {
		t.Fatalf("a placement_position block_face trait gives 6 states, got %d", len(states))
	}
}

// A server numbering by network hashes (BDS with block-network-ids-are-hashes) hashes its add-on
// blocks' states too: they map to the union's ids and back, instead of to air.
func TestHashedPaletteNumbersCustomBlocks(t *testing.T) {
	blocks := []protocol.BlockEntry{
		{Name: "oresplus:viocrite_bricks", Properties: map[string]any{}},
		{Name: "oresplus:rose_quartz_pillar", Properties: map[string]any{
			"traits": []any{map[string]any{"name": "minecraft:placement_position",
				"enabled_states": map[string]any{"block_face": uint8(1), "vertical_half": uint8(0)}}},
		}},
	}
	server, err := newHashedBlockPalette(blocks)
	if err != nil {
		t.Fatal(err)
	}
	union, err := newBlockPalette(blocks)
	if err != nil {
		t.Fatal(err)
	}
	m := newBlockMap(server, union)
	for _, c := range []struct {
		name  string
		props map[string]any
	}{
		{"oresplus:viocrite_bricks", map[string]any{}},
		{"oresplus:rose_quartz_pillar", map[string]any{"minecraft:block_face": "east"}},
		{"minecraft:stone", map[string]any{}},
	} {
		h := networkBlockHash(c.name, c.props)
		want, ok := union.index[blockState{Name: c.name, Properties: encodeProperties(c.props)}]
		if !ok {
			t.Fatalf("%s %v not in the union palette", c.name, c.props)
		}
		if got := m.forward(h); got != want {
			t.Errorf("%s %v: hash %#x maps to %v, want %v", c.name, c.props, h, union.states[got], union.states[want])
		}
		if back := m.reverse(want); back != h {
			t.Errorf("%s %v: maps back to %#x, want %#x", c.name, c.props, back, h)
		}
	}
}

// TestMultiBlockTrait: a multi_block trait adds a part state, one value per part, and a trait
// state the relay does not know is reported instead of being left out silently.
func TestMultiBlockTrait(t *testing.T) {
	curtain := protocol.BlockEntry{Name: "x:curtain", Properties: map[string]any{
		"properties": []any{map[string]any{"name": "x:open", "enum": []any{uint8(0), uint8(1)}}},
		"traits": []any{
			map[string]any{"name": "minecraft:placement_direction", "enabled_states": map[string]any{"cardinal_direction": uint8(1), "facing_direction": uint8(0)}},
			map[string]any{"name": "minecraft:multi_block", "enabled_states": map[string]any{"multi_block": uint8(1)}, "part_count": int32(2), "direction": int32(1)},
		},
	}}
	states, err := customBlockStates(curtain)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2*4*2 {
		t.Fatalf("open x 4 directions x 2 parts = 16 states, got %d", len(states))
	}
	if u := UnknownBlockTraits([]protocol.BlockEntry{curtain}); len(u) != 0 {
		t.Fatalf("unknown traits %v", u)
	}
	odd := protocol.BlockEntry{Name: "x:odd", Properties: map[string]any{
		"traits": []any{map[string]any{"name": "minecraft:placement_direction", "enabled_states": map[string]any{"sixteen_way_rotation": uint8(1)}}},
	}}
	if u := UnknownBlockTraits([]protocol.BlockEntry{odd, curtain}); u["minecraft:placement_direction/sixteen_way_rotation"] != 1 {
		t.Fatalf("the unknown trait state is not reported: %v", u)
	}
}

// TestTraitStatesComeFirst: a block's trait states change faster than its own properties, as a
// BDS numbers them. Its numbers for the test add-on blocks (hashes off, 2026-11-16): zz:both with
// properties listed [zz:a (2 values), aa:b (3)] and a cardinal direction had west alone at +1,
// zz:a=1 alone at +4 and aa:b=1 alone at +8; a two-part curtain had the part at +1, north at +4,
// open at +8.
func TestTraitStatesComeFirst(t *testing.T) {
	both := protocol.BlockEntry{Name: "zz:both", Properties: map[string]any{
		"properties": []any{
			map[string]any{"name": "zz:a", "enum": []any{int32(0), int32(1)}},
			map[string]any{"name": "aa:b", "enum": []any{int32(0), int32(1), int32(2)}},
		},
		"traits": []any{map[string]any{"name": "minecraft:placement_direction", "enabled_states": map[string]any{"cardinal_direction": uint8(1)}}},
	}}
	states, err := customBlockStateProps(both)
	if err != nil {
		t.Fatal(err)
	}
	at := func(want map[string]any) int {
		for i, s := range states {
			ok := true
			for k, v := range want {
				if fmt.Sprint(s[k]) != fmt.Sprint(v) {
					ok = false
				}
			}
			if ok {
				return i
			}
		}
		return -1
	}
	for _, c := range []struct {
		state map[string]any
		want  int
	}{
		{map[string]any{"minecraft:cardinal_direction": "west", "zz:a": 0, "aa:b": 0}, 1},
		{map[string]any{"minecraft:cardinal_direction": "south", "zz:a": 1, "aa:b": 0}, 4},
		{map[string]any{"minecraft:cardinal_direction": "south", "zz:a": 0, "aa:b": 1}, 8},
	} {
		if got := at(c.state); got != c.want {
			t.Errorf("%v is state %d, want %d", c.state, got, c.want)
		}
	}
	curtain := protocol.BlockEntry{Name: "x:curtain", Properties: map[string]any{
		"properties": []any{map[string]any{"name": "x:open", "enum": []any{uint8(0), uint8(1)}}},
		"traits": []any{
			map[string]any{"name": "minecraft:multi_block", "enabled_states": map[string]any{"multi_block": uint8(1)}, "part_count": int32(2)},
			map[string]any{"name": "minecraft:placement_direction", "enabled_states": map[string]any{"cardinal_direction": uint8(1)}},
		},
	}}
	if states, err = customBlockStateProps(curtain); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		state map[string]any
		want  int
	}{
		{map[string]any{"minecraft:multi_block_part": 1, "minecraft:cardinal_direction": "south", "x:open": 0}, 1},
		{map[string]any{"minecraft:multi_block_part": 0, "minecraft:cardinal_direction": "north", "x:open": 0}, 4},
		{map[string]any{"minecraft:multi_block_part": 0, "minecraft:cardinal_direction": "south", "x:open": 1}, 8},
	} {
		if got := at(c.state); got != c.want {
			t.Errorf("%v is state %d, want %d", c.state, got, c.want)
		}
	}
}
