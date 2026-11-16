package bridge

import (
	"sort"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// uniqueBlockIDs numbers the custom blocks of the union again: the ones numbered from 10000
// (vanilla_block_data block_id) get 10000, 10001, 10002... without gaps, server by server (server[i]
// is the place in the server order of the server blocks[i] came from) and within a server in the
// order of that server's own numbers. Each server numbers its own custom blocks from 10000, so in the
// union Dragonfly's Ores Plus blocks and Geyser's player skull blocks shared numbers, and the client
// drew one block of each such pair's item as nothing. Giving only the duplicates new numbers past the
// end broke more: Bedrock chest menus on PvP stopped opening. The first server keeps its own numbers
// (its blocks are numbered 10000 on without gaps), the others follow on in their own order. BDS lists
// its blocks in no particular order, so the list order is not the order of its numbers: numbering by
// list position gave every Ores Plus block another one's number. Entries numbered below 10000
// (vanilla-style ids) keep theirs, as do entries without the field (removing it re-sorted the client's
// palette: every block came out wrong). The ids are only the client's: the relay maps blocks by name
// and the servers never see the union's numbers. But a block item whose network id is negative is the
// block with legacy id 255-id to the client (BDS numbers its add-on block items that way), so
// blockItemIDs gives such items the new numbers too. It returns each renumbered block's new id.
func uniqueBlockIDs(blocks []protocol.BlockEntry, server []int) map[string]int64 {
	type entry struct {
		i      int
		server int
		id     int64
	}
	var custom []entry
	for i, b := range blocks {
		id, ok := blockID(b)
		if !ok || id < 10000 {
			continue
		}
		s := 0
		if i < len(server) {
			s = server[i]
		}
		custom = append(custom, entry{i, s, id})
	}
	sort.SliceStable(custom, func(a, b int) bool {
		if custom[a].server != custom[b].server {
			return custom[a].server < custom[b].server
		}
		return custom[a].id < custom[b].id
	})
	ids := make(map[string]int64, len(custom))
	for k, e := range custom {
		next := int64(10000 + k)
		if e.id != next {
			blocks[e.i].Properties = withBlockID(blocks[e.i].Properties, next)
		}
		ids[blocks[e.i].Name] = next
	}
	return ids
}

// blockItemIDs gives the union's legacy-numbered block items (legacy[name]: the item's network id was
// negative on its server, as BDS numbers add-on block items: 255 - block id) the network id that
// points the client at their block's union number, 255 - ids[name]. An item another server numbered
// the same is moved to a free number from next on. It returns the next free number.
func blockItemIDs(items []protocol.ItemEntry, ids map[string]int64, legacy map[string]bool, next int16) int16 {
	taken := map[int16]bool{}
	for i := range items {
		it := &items[i]
		id, ok := ids[it.Name]
		if !ok || !legacy[it.Name] || 255-id < -32768 {
			continue
		}
		it.RuntimeID = int16(255 - id)
		taken[it.RuntimeID] = true
	}
	for i := range items {
		it := &items[i]
		if !taken[it.RuntimeID] {
			continue
		}
		if id, ok := ids[it.Name]; ok && legacy[it.Name] && int64(it.RuntimeID) == 255-id {
			continue
		}
		it.RuntimeID = next
		next++
	}
	return next
}

func blockID(b protocol.BlockEntry) (int64, bool) {
	v, ok := b.Properties["vanilla_block_data"].(map[string]any)
	if !ok {
		return 0, false
	}
	switch n := v["block_id"].(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case int16:
		return int64(n), true
	case uint8:
		return int64(n), true
	}
	return 0, false
}

// withBlockID copies props with vanilla_block_data's block_id set to id, keeping its NBT type.
func withBlockID(props map[string]any, id int64) map[string]any {
	out := make(map[string]any, len(props))
	for k, v := range props {
		out[k] = v
	}
	old := props["vanilla_block_data"].(map[string]any)
	vbd := make(map[string]any, len(old))
	for k, v := range old {
		vbd[k] = v
	}
	switch old["block_id"].(type) {
	case int64:
		vbd["block_id"] = id
	case int16:
		vbd["block_id"] = int16(id)
	default:
		vbd["block_id"] = int32(id)
	}
	out["vanilla_block_data"] = vbd
	return out
}
