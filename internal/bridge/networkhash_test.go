package bridge

import "testing"

// The network block hash of air is fixed by the client (-604749536); stone and an oak log are
// checked against Dragonfly's world.networkBlockHash.
func TestNetworkBlockHash(t *testing.T) {
	for _, c := range []struct {
		name  string
		props map[string]any
		want  int32
	}{
		{"minecraft:air", map[string]any{}, -604749536},
		{"minecraft:stone", map[string]any{}, -2144268767},
		{"minecraft:oak_log", map[string]any{"pillar_axis": "y"}, 825916963},
	} {
		if got := int32(networkBlockHash(c.name, c.props)); got != c.want {
			t.Errorf("%s hashes to %d, want %d", c.name, got, c.want)
		}
	}
	if len(vanillaHashes) != len(vanillaStates) {
		t.Fatalf("%d hashes for %d states", len(vanillaHashes), len(vanillaStates))
	}
}
