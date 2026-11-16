package bridge

import (
	"io"
	"log/slog"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func numberedBlock(name string, id int32) protocol.BlockEntry {
	return protocol.BlockEntry{Name: name, Properties: map[string]any{
		"vanilla_block_data": map[string]any{"block_id": id},
	}}
}

// A server's own numbers survive: BDS lists its add-on blocks in no particular order, and numbering
// them by list position gave every block another one's number.
func TestUniqueBlockIDsKeepsTheFirstServersNumbers(t *testing.T) {
	blocks := []protocol.BlockEntry{
		numberedBlock("oresplus:limestone_lapis_ore", 10002),
		numberedBlock("minecraft:black_wool_slab", 1423), // vanilla-style id: kept
		numberedBlock("oresplus:viocrite_bricks", 10000),
		numberedBlock("oresplus:sapphire_ore", 10001),
	}
	ids := uniqueBlockIDs(blocks, []int{0, 0, 0, 0})
	for i, want := range []int64{10002, 1423, 10000, 10001} {
		if got, _ := blockID(blocks[i]); got != want {
			t.Errorf("%s: block_id %d, want %d", blocks[i].Name, got, want)
		}
	}
	if len(ids) != 3 || ids["oresplus:viocrite_bricks"] != 10000 {
		t.Errorf("ids = %v", ids)
	}
}

// A later server's blocks follow on after the first one's, in the order of its own numbers.
func TestUniqueBlockIDsSecondServerFollowsOn(t *testing.T) {
	blocks := []protocol.BlockEntry{
		numberedBlock("geyser_custom:oresplus_ruby_ore", 10001),
		numberedBlock("geyser_custom:player_skull_a", 10000),
		numberedBlock("oresplus:sapphire_ore", 10001),
		numberedBlock("oresplus:viocrite_bricks", 10000),
	}
	ids := uniqueBlockIDs(blocks, []int{0, 0, 1, 1})
	want := map[string]int64{
		"geyser_custom:player_skull_a":    10000,
		"geyser_custom:oresplus_ruby_ore": 10001,
		"oresplus:viocrite_bricks":        10002,
		"oresplus:sapphire_ore":           10003,
	}
	for _, b := range blocks {
		if got, _ := blockID(b); got != want[b.Name] || ids[b.Name] != want[b.Name] {
			t.Errorf("%s: block_id %d (ids %d), want %d", b.Name, got, ids[b.Name], want[b.Name])
		}
	}
}

// BDS numbers an add-on block's item 255 - the block's id: the client takes such an item for the
// block with that legacy id, so the item follows its block's union number.
func TestBlockItemIDsFollowTheirBlock(t *testing.T) {
	items := []protocol.ItemEntry{
		{Name: "oresplus:viocrite_bricks", RuntimeID: 255 - 10036},
		{Name: "oresplus:sapphire_ore", RuntimeID: 255 - 10023},
		{Name: "geyser_custom:oresplus_ruby_ore", RuntimeID: 2300}, // Dragonfly/Geyser: own number
		{Name: "zid:other", RuntimeID: 255 - 10001},                // sits on a number a block item needs
	}
	ids := map[string]int64{
		"oresplus:viocrite_bricks":        10001,
		"oresplus:sapphire_ore":           10000,
		"geyser_custom:oresplus_ruby_ore": 10002,
	}
	legacy := map[string]bool{"oresplus:viocrite_bricks": true, "oresplus:sapphire_ore": true, "zid:other": true}
	next := blockItemIDs(items, ids, legacy, 5000)
	want := []int16{255 - 10001, 255 - 10000, 2300, 5000}
	for i, w := range want {
		if items[i].RuntimeID != w {
			t.Errorf("%s: id %d, want %d", items[i].Name, items[i].RuntimeID, w)
		}
	}
	if next != 5001 {
		t.Errorf("next = %d, want 5001", next)
	}
}

// The union of a Dragonfly-like server (first) and a BDS one (hashed, shuffled list, negative block
// items): every BDS block item points at its own block's union number.
func TestUnionBlockItemsMatchTheirBlocks(t *testing.T) {
	n := &Network{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		content: map[string]*serverContent{},
		packs:   map[string]*cachedPack{},
	}
	if err := n.setLive([]ServerConfig{{Name: "df"}, {Name: "bds"}}, []string{"df"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	n.content["df"] = &serverContent{
		StartGame: &packet.StartGame{Blocks: []protocol.BlockEntry{
			numberedBlock("geyser_custom:oresplus_ruby_ore", 10000),
			numberedBlock("zid:skystone", 10001),
		}},
		Items: &packet.ItemRegistry{Items: []protocol.ItemEntry{
			{Name: "minecraft:stone", RuntimeID: 1},
			{Name: "geyser_custom:oresplus_ruby_ore", RuntimeID: 2300},
			{Name: "zid:skystone", RuntimeID: 2301},
		}},
	}
	bdsBlocks := map[string]int32{"oresplus:limestone_lapis_ore": 10049, "oresplus:viocrite_bricks": 10036, "oresplus:sapphire_ore": 10023, "oresplus:deepslate_ambar_ore": 10000}
	sg := &packet.StartGame{UseBlockNetworkIDHashes: true}
	reg := &packet.ItemRegistry{Items: []protocol.ItemEntry{{Name: "minecraft:stone", RuntimeID: 1}}}
	for _, name := range []string{"oresplus:limestone_lapis_ore", "oresplus:viocrite_bricks", "oresplus:sapphire_ore", "oresplus:deepslate_ambar_ore"} {
		sg.Blocks = append(sg.Blocks, numberedBlock(name, bdsBlocks[name]))
		reg.Items = append(reg.Items, protocol.ItemEntry{Name: name, RuntimeID: int16(255 - bdsBlocks[name])})
	}
	n.content["bds"] = &serverContent{StartGame: sg, Items: reg}
	u, err := n.makeUnion("")
	if err != nil {
		t.Fatal(err)
	}
	blockIDs := map[string]int64{}
	seen := map[int64]string{}
	for _, b := range u.blocks {
		id, _ := blockID(b)
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s share block_id %d", b.Name, other, id)
		}
		seen[id], blockIDs[b.Name] = b.Name, id
	}
	if blockIDs["geyser_custom:oresplus_ruby_ore"] != 10000 || blockIDs["zid:skystone"] != 10001 {
		t.Errorf("the first server's blocks were renumbered: %v", blockIDs)
	}
	itemIDs := map[int16]string{}
	for _, it := range u.items.Items {
		if other, dup := itemIDs[it.RuntimeID]; dup {
			t.Errorf("%s and %s share item id %d", it.Name, other, it.RuntimeID)
		}
		itemIDs[it.RuntimeID] = it.Name
		switch {
		case it.RuntimeID < 0:
			if 255-int64(it.RuntimeID) != blockIDs[it.Name] {
				t.Errorf("%s: item %d is block_id %d to the client, its block has %d", it.Name, it.RuntimeID, 255-int64(it.RuntimeID), blockIDs[it.Name])
			}
		case it.Name == "geyser_custom:oresplus_ruby_ore" && it.RuntimeID != 2300:
			t.Errorf("Dragonfly block item renumbered to %d", it.RuntimeID)
		}
	}
	if n := len(u.items.Items); n != 7 {
		t.Errorf("%d items, want 7", n)
	}
}
