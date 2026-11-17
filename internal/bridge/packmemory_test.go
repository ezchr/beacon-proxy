package bridge

import (
	"slices"
	"testing"
)

// A player whose client took lulu's packs gets them again at the next join, headed anywhere: no
// join again to go to lulu, until lulu's packs change.
func TestKnownOnJoinPacksComeAtJoin(t *testing.T) {
	n := packsNetwork(t)
	n.Dir = t.TempDir()
	v, prints := n.withKnownPacks("222", n.unionVariant("lulu"))
	if v != "lulu" || prints["lulu"] == "" {
		t.Fatalf("join for lulu: %q %v", v, prints)
	}
	n.rememberPacks("222", prints)

	// Next join, to kits: lulu's packs come too (and skygen's do not: never taken, and closed).
	n2 := packsNetwork(t)
	n2.Dir = n.Dir
	v, _ = n2.withKnownPacks("222", n2.unionVariant("kits"))
	if v != "lulu" {
		t.Fatalf("join for kits with lulu's packs known: %q", v)
	}
	if n2.packRejoinNeeded(*n2.server("lulu"), v) {
		t.Fatal("lulu needs a join again although the client has its packs")
	}
	var got []string
	for _, p := range unionPacks(t, n2, v).packsInfo.TexturePacks {
		got = append(got, p.UUID.String())
	}
	if !slices.Contains(got, luluPack.String()) || !slices.Contains(got, kitsPack.String()) || slices.Contains(got, skygenPack.String()) {
		t.Fatalf("packs %v", got)
	}

	// lulu updates its pack: the join leaves it out, going there joins again for the new one.
	n2.content["lulu"].PacksInfo.TexturePacks[0].Version = "1.0.1"
	n2.packs[luluPack.String()+"_1.0.1"] = &cachedPack{data: []byte{2}}
	if v, _ = n2.withKnownPacks("222", n2.unionVariant("kits")); v != "" {
		t.Fatalf("updated lulu still in the join: %q", v)
	}
	if !n2.packRejoinNeeded(*n2.server("lulu"), "") {
		t.Fatal("no join again for lulu's new pack")
	}
}

// Two on-join servers' packs at once, in one shared union variant.
func TestTwoOnJoinVariants(t *testing.T) {
	n := packsNetwork(t)
	if v := n.normVariant("skygen,LULU,kits,lulu"); v != "lulu,skygen" {
		t.Fatalf("norm %q", v)
	}
	u := unionPacks(t, n, "lulu,skygen")
	if len(u.packsInfo.TexturePacks) != 3 {
		t.Fatalf("%d packs, want kits+lulu+skygen", len(u.packsInfo.TexturePacks))
	}
	if n.packRejoinNeeded(*n.server("skygen"), "lulu,skygen") || n.packRejoinNeeded(*n.server("lulu"), "lulu,skygen") {
		t.Fatal("join again for packs the client has")
	}
	s, _ := packsSession(n, "111", "lulu,skygen")
	s.onJoinGens = map[string]uint64{"lulu": 0, "skygen": 0}
	n.onJoinGenOf("skygen").Add(1)
	if !s.packsOutdated(*n.server("skygen")) || s.packsOutdated(*n.server("lulu")) || s.packsChanged("kits") {
		t.Fatal("a change to skygen's packs counts for the wrong servers")
	}
}
