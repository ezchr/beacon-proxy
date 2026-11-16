package bridge

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

var (
	kitsPack   = uuid.MustParse("00000000-0000-4000-8000-00000000000a")
	luluPack   = uuid.MustParse("00000000-0000-4000-8000-00000000000b")
	skygenPack = uuid.MustParse("00000000-0000-4000-8000-00000000000c")
	oldPack    = uuid.MustParse("00000000-0000-4000-8000-00000000000d")
)

// packsNetwork is a network of kits (default), lulu and skygen (resource-packs = "on-join", skygen
// closed to one XUID) and old (resource-packs = false), each with one downloaded pack and a block.
func packsNetwork(t *testing.T) *Network {
	t.Helper()
	n := &Network{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		content: map[string]*serverContent{},
		packs:   map[string]*cachedPack{},
	}
	servers := []ServerConfig{
		{Name: "kits"},
		{Name: "lulu", PacksOnJoin: true},
		{Name: "skygen", PacksOnJoin: true, Allowed: []string{"111"}},
		{Name: "old", NoPacks: true},
	}
	if err := n.setLive(servers, []string{"kits"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]uuid.UUID{"kits": kitsPack, "lulu": luluPack, "skygen": skygenPack, "old": oldPack} {
		n.content[name] = &serverContent{
			StartGame: &packet.StartGame{Blocks: []protocol.BlockEntry{{Name: name + ":block", Properties: map[string]any{}}}},
			Items:     &packet.ItemRegistry{},
			PacksInfo: &packet.ResourcePacksInfo{TexturePacks: []protocol.TexturePackInfo{{UUID: id, Version: "1.0.0"}}},
		}
		n.packs[id.String()+"_1.0.0"] = &cachedPack{data: []byte{1}}
	}
	return n
}

func unionPacks(t *testing.T, n *Network, variant string) *union {
	t.Helper()
	u, err := n.buildUnion(variant)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func packIDs(u *union) []string {
	var out []string
	for _, p := range u.packsInfo.TexturePacks {
		out = append(out, p.UUID.String())
	}
	var stack []string
	for _, p := range u.packStack.TexturePacks {
		stack = append(stack, p.UUID)
	}
	if !slices.Equal(out, stack) {
		return append(out, "stack differs: "+strings.Join(stack, ","))
	}
	return out
}

// The base union leaves the on-join servers' packs out, and their custom blocks with them; a login
// headed to one gets its packs and blocks, and only its. So a block list of one on-join server
// cannot change how the others' blocks are numbered.
func TestUnionVariants(t *testing.T) {
	n := packsNetwork(t)
	base := unionPacks(t, n, "")
	if got, want := packIDs(base), []string{kitsPack.String()}; !slices.Equal(got, want) {
		t.Errorf("base union packs %v, want %v", got, want)
	}
	lulu := unionPacks(t, n, "Lulu")
	if got, want := packIDs(lulu), []string{kitsPack.String(), luluPack.String()}; !slices.Equal(got, want) {
		t.Errorf("lulu union packs %v, want %v", got, want)
	}
	sky := unionPacks(t, n, "skygen")
	if got, want := packIDs(sky), []string{kitsPack.String(), skygenPack.String()}; !slices.Equal(got, want) {
		t.Errorf("skygen union packs %v, want %v", got, want)
	}
	has := func(u *union, name string) bool {
		return slices.ContainsFunc(u.blocks, func(b protocol.BlockEntry) bool { return b.Name == name+":block" })
	}
	for _, c := range []struct {
		u     *union
		which string
		want  map[string]bool
	}{
		{base, "base", map[string]bool{"kits": true, "old": true, "lulu": false, "skygen": false}},
		{lulu, "lulu", map[string]bool{"kits": true, "old": true, "lulu": true, "skygen": false}},
		{sky, "skygen", map[string]bool{"kits": true, "old": true, "lulu": false, "skygen": true}},
	} {
		for name, want := range c.want {
			if has(c.u, name) != want {
				t.Errorf("%s union has %s's block: %v, want %v", c.which, name, !want, want)
			}
		}
	}
	// A server that is not on-join (or unknown) is the base variant, and shares its cache.
	if u := unionPacks(t, n, "kits"); u != base {
		t.Error("kits did not get the cached base union")
	}
	if u := unionPacks(t, n, "nope"); u != base {
		t.Error("an unknown server did not get the cached base union")
	}
	if u := unionPacks(t, n, "lulu"); u != lulu {
		t.Error("the lulu union was built again")
	}
	// New content builds every variant again.
	n.mu.Lock()
	n.contentGen++
	n.mu.Unlock()
	if u := unionPacks(t, n, ""); u == base {
		t.Error("base union kept after a content change")
	}
	if u := unionPacks(t, n, "lulu"); u == lulu {
		t.Error("lulu union kept after a content change")
	}
}

// A move to an on-join server takes a join again only for a client that did not join for it: its
// packs and its custom blocks come with that join.
func TestPackRejoinNeeded(t *testing.T) {
	n := packsNetwork(t)
	lulu, sky, kits, old := *n.server("lulu"), *n.server("skygen"), *n.server("kits"), *n.server("old")
	for _, c := range []struct {
		to   ServerConfig
		have string
		want bool
	}{
		{lulu, "", true},
		{lulu, "lulu", false},
		{lulu, "LULU", false},
		{sky, "lulu", true}, // lulu -> skygen: skygen's packs are not in a lulu join's union
		{kits, "", false},
		{kits, "lulu", false}, // moving away from an on-join server needs nothing
		{old, "", false},
	} {
		if got := n.packRejoinNeeded(c.to, c.have); got != c.want {
			t.Errorf("to %s with %q: %v, want %v", c.to.Name, c.have, got, c.want)
		}
	}
	// Without its packs downloaded the join again still brings its custom blocks.
	delete(n.packs, luluPack.String()+"_1.0.0")
	if !n.packRejoinNeeded(lulu, "") {
		t.Error("no join again for a server whose custom blocks the client lacks")
	}
	// A server with neither packs to give nor custom blocks needs none.
	n.mu.Lock()
	n.content["lulu"].StartGame.Blocks = nil
	n.mu.Unlock()
	if n.packRejoinNeeded(lulu, "") {
		t.Error("a join again for a server with nothing to give")
	}
}

// Pack changes of an on-join server only send its own players to join again.
func TestPackGenFor(t *testing.T) {
	n := packsNetwork(t)
	base, lulu := n.packGenFor(""), n.packGenFor("lulu")
	n.onJoinGenOf("Lulu").Add(1)
	if n.packGenFor("") != base || n.packGenFor("skygen") != base {
		t.Error("lulu's new packs changed other clients' generation")
	}
	if n.packGenFor("lulu") == lulu {
		t.Error("lulu's new packs did not change its clients' generation")
	}
	n.packGen.Add(1)
	if n.packGenFor("") == base {
		t.Error("a server's new packs did not change the base generation")
	}
}

// packsSession is a session of xuid on the network, its messages kept, already ended (so a join
// again stops before writing to a client).
func packsSession(n *Network, xuid, packsFor string) (*switchSession, *[]string) {
	var msgs []string
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(nil)
	s := &switchSession{
		n: n, log: n.Log, ctx: ctx, cancel: cancel, packsFor: packsFor,
		reply: func(m string) { msgs = append(msgs, m) },
	}
	s.id.XUID = xuid
	return s, &msgs
}

// /server lulu from a client without lulu's packs: no move in place, a join again that lands on lulu.
func TestSwitchToOnJoinServerRejoins(t *testing.T) {
	n := packsNetwork(t)
	s, msgs := packsSession(n, "222", "")
	s.switchTo(*n.server("lulu"), "")
	if !s.rejoining.Load() {
		t.Fatal("no join again")
	}
	if got := n.takeRejoin("222"); got != "lulu" {
		t.Fatalf("join again lands on %q, want lulu", got)
	}
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "Downloading Lulu's resource pack") {
		t.Fatalf("messages %q", *msgs)
	}
	// The join again is headed to lulu: it gets lulu's packs.
	if v := n.unionVariant("lulu"); v != "lulu" {
		t.Fatalf("variant %q", v)
	}
}

// A closed server (allowed) refuses everyone else on every path, with no join again for its packs.
func TestClosedServerRefused(t *testing.T) {
	n := packsNetwork(t)
	sky := *n.server("skygen")
	s, msgs := packsSession(n, "222", "")
	if err := s.checkSwitch(sky, ""); err == nil || err.Error() != closedServerMessage {
		t.Fatalf("checkSwitch: %v", err)
	}
	if err := s.requestSwitch(sky, ""); err == nil || err.Error() != closedServerMessage {
		t.Fatalf("requestSwitch: %v", err)
	}
	s.switchTo(sky, "")
	if s.rejoining.Load() || n.takeRejoin("222") != "" {
		t.Fatal("a player not allowed was sent to join again for the closed server's packs")
	}
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], closedServerMessage) {
		t.Fatalf("messages %q", *msgs)
	}
	if slices.Contains(n.serverNamesFor("222"), "skygen") || !slices.Contains(n.serverNamesFor("111"), "skygen") {
		t.Fatalf("suggestions: %v / %v", n.serverNamesFor("222"), n.serverNamesFor("111"))
	}
	order := allowedServers(n.withFirstServer(n.joinOrder(2225, ""), "skygen", 2225), "222")
	if slices.ContainsFunc(order, func(s ServerConfig) bool { return s.Name == "skygen" }) {
		t.Fatalf("a join again to the closed server kept it in order: %v", serverNames(order))
	}
	// Its own player is sent to join again for its packs.
	a, _ := packsSession(n, "111", "")
	a.switchTo(sky, "")
	if n.takeRejoin("111") != "skygen" {
		t.Fatal("an allowed player was not sent to join again for skygen's packs")
	}
}

// A player whose server went away goes first to a server their client can play on as it is.
func TestMoveAwayPrefersServersWithoutRejoin(t *testing.T) {
	n := packsNetwork(t)
	s, _ := packsSession(n, "222", "")
	if got := serverNames(s.moveAwayOrder("kits")); !slices.Equal(got, []string{"old", "lulu", "skygen"}) {
		t.Errorf("without packs: %v", got)
	}
	s.packsFor = "lulu"
	if got := serverNames(s.moveAwayOrder("kits")); !slices.Equal(got, []string{"lulu", "old", "skygen"}) {
		t.Errorf("with lulu's packs: %v", got)
	}
}
