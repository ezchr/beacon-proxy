package bridge

import (
	"bytes"
	_ "embed"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// The block palette: what a client numbers block states by. With block network id hashes off (both
// Dragonfly and Geyser leave them off), a block's runtime id is its index in the list of every
// vanilla block state plus the states of the custom blocks the server announced in StartGame, sorted
// (stably) by the FNV-1 64-bit hash of the block name. Two servers with different custom blocks so
// number most blocks differently, and the relay renumbers between them.

// vanillaBlockStates is every vanilla block state of the relay's protocol, in order, as network
// NBT (Dragonfly's block_states.nbt for 1.26.60). It must be updated with the protocol version.
//
//go:embed block_states.nbt
var vanillaBlockStates []byte

// blockState is one entry of a palette: a block name and its encoded properties.
type blockState struct {
	Name       string
	Properties string // canonical encoding, see encodeProperties
}

// vanillaStates is the decoded vanillaBlockStates, and vanillaNames the set of names in it.
var vanillaStates, vanillaNames = decodeBlockStates(vanillaBlockStates)

// vanillaFor returns the vanilla states and names a client of protocol proto numbers blocks by.
func vanillaFor(proto int32) ([]blockState, map[string]bool) {
	return vanillaStates, vanillaNames
}

func decodeBlockStates(data []byte) ([]blockState, map[string]bool) {
	dec := nbt.NewDecoder(bytes.NewBuffer(data))
	var states []blockState
	names := map[string]bool{}
	for {
		var s struct {
			Name       string         `nbt:"name"`
			Properties map[string]any `nbt:"states"`
			Version    int32          `nbt:"version"`
		}
		if err := dec.Decode(&s); err != nil {
			break
		}
		states = append(states, blockState{Name: s.Name, Properties: encodeProperties(s.Properties)})
		names[s.Name] = true
	}
	return states, names
}

// encodeProperties encodes block properties canonically, so equal states encode equally.
func encodeProperties(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v;", k, m[k])
	}
	return b.String()
}

// blockPalette is the full numbering a client uses for one set of custom blocks.
type blockPalette struct {
	states []blockState
	index  map[blockState]uint32
	// custom is the sorted names of the non-vanilla custom blocks, which identify the palette.
	custom []string
	// vanilla is the number of vanilla states, which differs between protocol versions.
	vanilla int

	namesOnce sync.Once
	names     map[string][]uint32
}

// newBlockPalette builds the palette a client of the relay's protocol builds from StartGame's custom
// block list.
func newBlockPalette(blocks []protocol.BlockEntry) (*blockPalette, error) {
	return newBlockPaletteFor(blocks, 0)
}

// newBlockPaletteFor builds the palette a client of protocol proto builds from StartGame's custom
// block list.
func newBlockPaletteFor(blocks []protocol.BlockEntry, proto int32) (*blockPalette, error) {
	vanilla, names := vanillaFor(proto)
	states := slices.Clone(vanilla)
	var custom []string
	for _, b := range blocks {
		if names[b.Name] {
			// A vanilla data-driven block: StartGame only carries its components, the states are
			// already in the vanilla list.
			continue
		}
		perms, err := customBlockStates(b)
		if err != nil {
			return nil, fmt.Errorf("custom block %s: %w", b.Name, err)
		}
		states = append(states, perms...)
		custom = append(custom, b.Name)
	}
	sort.SliceStable(states, func(i, j int) bool {
		return fnv164(states[i].Name) < fnv164(states[j].Name)
	})
	p := &blockPalette{states: states, index: make(map[blockState]uint32, len(states)), custom: custom, vanilla: len(vanilla)}
	sort.Strings(p.custom)
	for i, s := range states {
		p.index[s] = uint32(i)
	}
	return p, nil
}

// customBlockStates lists a custom block's states in the order the client registers them: every
// combination of its properties' values, the FIRST property changing fastest (as Geyser generates
// them, matching the client). Property values come as whatever NBT list type the server used
// (strings, ints, bytes for booleans).
func customBlockStates(b protocol.BlockEntry) ([]blockState, error) {
	props, _ := b.Properties["properties"].([]any)
	type prop = blockProp
	var ps []prop
	for _, p := range props {
		m, ok := p.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("property is %T", p)
		}
		name, _ := m["name"].(string)
		values := anySlice(m["enum"])
		if name == "" || len(values) == 0 {
			return nil, fmt.Errorf("property %v has no name or values", m)
		}
		ps = append(ps, prop{name, values})
	}
	// Traits add states of their own (a BDS add-on's pillar: 6 block faces), which the client
	// numbers like declared properties: without them every block sorted after is off.
	ps = append(ps, traitProps(b)...)
	if len(ps) == 0 {
		return []blockState{{Name: b.Name, Properties: encodeProperties(nil)}}, nil
	}
	total := 1
	for _, p := range ps {
		total *= len(p.values)
	}
	out := make([]blockState, 0, total)
	for i := 0; i < total; i++ {
		cur := make(map[string]any, len(ps))
		idx := i
		for _, p := range ps {
			cur[p.name] = p.values[idx%len(p.values)]
			idx /= len(p.values)
		}
		out = append(out, blockState{Name: b.Name, Properties: encodeProperties(cur)})
	}
	return out, nil
}

// blockProp is one block state and its values, in the order the client lists them.
type blockProp struct {
	name   string
	values []any
}

// traitStates are the states each block trait can enable, in the order the client adds them.
var traitStates = map[string][]blockProp{
	"minecraft:placement_position": {
		{"minecraft:block_face", []any{"down", "up", "north", "south", "west", "east"}},
		{"minecraft:vertical_half", []any{"bottom", "top"}},
	},
	"minecraft:placement_direction": {
		{"minecraft:cardinal_direction", []any{"south", "west", "north", "east"}},
		{"minecraft:facing_direction", []any{"down", "up", "north", "south", "west", "east"}},
	},
}

// traitProps lists the states a custom block's traits enable ("traits" in its StartGame entry).
func traitProps(b protocol.BlockEntry) []blockProp {
	var out []blockProp
	for _, t := range anySlice(b.Properties["traits"]) {
		m, _ := t.(map[string]any)
		name, _ := m["name"].(string)
		enabled, _ := m["enabled_states"].(map[string]any)
		for _, st := range traitStates[name] {
			short := st.name[len("minecraft:"):]
			if on, ok := enabled[short]; ok && fmt.Sprint(on) != "0" && fmt.Sprint(on) != "false" {
				out = append(out, st)
			}
		}
	}
	return out
}

// anySlice returns the elements of any slice (an NBT list decodes to []any, []int32, []string...).
func anySlice(v any) []any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out
}

// fnv164 is the FNV-1 64-bit hash (multiply, then xor) the palette is sorted by.
func fnv164(s string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(s); i++ {
		h *= 1099511628211
		h ^= uint64(s[i])
	}
	return h
}

// sameCustomBlocks reports whether two palettes number every block the same.
func (p *blockPalette) sameAs(o *blockPalette) bool {
	return slices.Equal(p.custom, o.custom) && p.vanilla == o.vanilla && len(p.states) == len(o.states)
}

// blockMap renumbers runtime ids of one palette to another. A state missing from the target maps to
// its air id.
type blockMap struct {
	to      []uint32 // from id -> to id
	back    []uint32 // to id -> from id
	fromAir uint32   // air in the from palette
	toAir   uint32   // air in the to palette
}

// isAir reports whether id is air: in the target palette if to, else in the source one.
func (m *blockMap) isAir(id uint32, to bool) bool {
	if to {
		return id == m.fromAir
	}
	return id == m.toAir
}

func newBlockMap(from, to *blockPalette) *blockMap {
	air := to.index[blockState{Name: "minecraft:air", Properties: encodeProperties(nil)}]
	fromAir := from.index[blockState{Name: "minecraft:air", Properties: encodeProperties(nil)}]
	m := &blockMap{to: make([]uint32, len(from.states)), back: make([]uint32, len(to.states)), fromAir: fromAir, toAir: air}
	for i := range m.back {
		m.back[i] = fromAir
	}
	// A custom block with one state on both sides matches by name: Dragonfly announces the Ores
	// Plus blocks without properties, Geyser with its fixed note block ones (instrument=harp;
	// note=N;powered=true), and an exact match only would turn them into air (invisible items).
	single := map[string]uint32{}
	count := map[string]int{}
	for i, s := range to.states {
		if count[s.Name]++; count[s.Name] == 1 {
			single[s.Name] = uint32(i)
		}
	}
	fromCount := map[string]int{}
	for _, s := range from.states {
		fromCount[s.Name]++
	}
	for i, s := range from.states {
		id, ok := to.index[s]
		if !ok && count[s.Name] == 1 && fromCount[s.Name] == 1 {
			id, ok = single[s.Name], true
		}
		if !ok {
			id, ok = closestState(s, to)
		}
		if !ok {
			id = air
		}
		m.to[i] = id
		if ok {
			m.back[id] = uint32(i)
		}
	}
	// States only the target has, such as a block that got properties in a newer version (1.26.60
	// added connections to redstone wire, fire and chorus plants), go back to the closest source one.
	for id, s := range to.states {
		if m.back[id] != fromAir || s.Name == "minecraft:air" {
			continue
		}
		if i, ok := closestState(s, from); ok {
			m.back[id] = i
		}
	}
	return m
}

// closestState finds the state of p with the same name whose properties include all of s's, or are
// all included in s's, sharing the most properties (the first such state on a tie: the block's
// default values come first). It matches a block whose properties changed between protocol versions.
func closestState(s blockState, p *blockPalette) (uint32, bool) {
	want := propertySet(s.Properties)
	best, bestN := uint32(0), -1
	for _, i := range p.byName()[s.Name] {
		have := propertySet(p.states[i].Properties)
		n := 0
		for k := range want {
			if have[k] {
				n++
			}
		}
		if (n == len(want) || n == len(have)) && n > bestN {
			best, bestN = i, n
		}
	}
	return best, bestN >= 0
}

// propertySet splits encoded properties into their "key=value" pairs.
func propertySet(props string) map[string]bool {
	set := map[string]bool{}
	for _, kv := range strings.Split(props, ";") {
		if kv != "" {
			set[kv] = true
		}
	}
	return set
}

// byName lists the ids of p's states by block name, in palette order.
func (p *blockPalette) byName() map[string][]uint32 {
	p.namesOnce.Do(func() {
		p.names = map[string][]uint32{}
		for i, s := range p.states {
			p.names[s.Name] = append(p.names[s.Name], uint32(i))
		}
	})
	return p.names
}

// forward renumbers a backend id for the client; reverse a client id for the backend.
func (m *blockMap) forward(id uint32) uint32 {
	if int(id) < len(m.to) {
		return m.to[id]
	}
	return id
}

func (m *blockMap) reverse(id uint32) uint32 {
	if int(id) < len(m.back) {
		return m.back[id]
	}
	return id
}

// PaletteNames lists the block names a client numbers by, given StartGame's custom blocks (for
// tests and tools).
func PaletteNames(blocks []protocol.BlockEntry) ([]string, error) {
	return PaletteNamesFor(blocks, 0)
}

// PaletteNamesFor is PaletteNames for a client of protocol proto.
func PaletteNamesFor(blocks []protocol.BlockEntry, proto int32) ([]string, error) {
	p, err := newBlockPaletteFor(blocks, proto)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(p.states))
	for i, s := range p.states {
		names[i] = s.Name
	}
	return names, nil
}

// ChunkPaletteIDs lists the block runtime ids in the palettes of n network-encoded sub-chunks (for
// tests and tools).
func ChunkPaletteIDs(data []byte, n int) ([]uint32, error) {
	var ids []uint32
	_, err := rewriteSubChunks(data, n, func(id uint32) uint32 {
		ids = append(ids, id)
		return id
	})
	return ids, err
}
