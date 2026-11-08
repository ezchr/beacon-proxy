package bridge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/df-mc/go-nethernet"
	"github.com/ezchr/beacon-proxy/internal/proxy"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// A Network is a set of backend servers the relay moves players between without them leaving the
// world (and its Friends-tab session): the relay stays connected to the player and swaps the server
// behind them, the way WaterdogPE does for Bedrock networks. See switchsession.go for the session
// and transfer.go for the move itself.
//
// The servers may differ in custom content. A Bedrock client takes resource packs, custom blocks,
// items, entity identifiers and experiments only once, at its first join, so the relay gives every
// player the union of all servers' content and renumbers per server where needed (rewrite.go). That
// union comes from what each server sent the last time anyone joined it, kept on disk (Dir), and the
// resource packs are downloaded from each server once and served by the relay itself.
type Network struct {
	// packGen counts the changes to the servers' resource packs: a client logged in before the
	// last one has old packs.
	packGen atomic.Uint64
	// contentGen counts the changes to what the union is built from (servers' content, packs).
	// With the live config it keys unionCache, the union every join shares. n.mu guards all three.
	contentGen uint64
	unionCache *union
	unionKey   unionKey
	// seen is what record last stored or found per server packet, palettes the block palettes of
	// servers' block lists with their maps to a union palette. n.mu guards both.
	seen     map[string]seenContent
	palettes map[paletteKey]serverPalette
	// rejoinTo holds, by XUID, the server a player was moving to when sent to join again for new
	// resource packs (switchSession.record): they land there when they are back, not on the default.
	rejoinTo sync.Map // xuid -> rejoinTarget

	// Servers in order; Default is where players join. Each needs a relay-mode door (a backend that
	// accepts the relay's re-signed logins).
	Servers []ServerConfig
	Default string
	// Try is the order players join in, falling back to the next server when one can't be joined
	// (default: Default only). ForcedHosts gives an address players type its own order.
	Try         []string
	ForcedHosts map[string][]string
	// ForwardingSecret, when set, signs a Beacon forwarding claim into every backend login.
	ForwardingSecret []byte
	// The key logins to forwarding = "endstone" servers are signed with, loaded at first use.
	endstoneOnce sync.Once
	endstoneK    *ecdsa.PrivateKey
	endstoneErr  error
	// Dir holds the per-server content cache and downloaded packs.
	Dir string
	// Identity mints NetherNet client identities for NetherNet servers.
	Identity func(ctx context.Context) (*nethernet.Identity, error)
	// Admins may use /broadcast. Listing switches the Friends-tab listing; nil disables /broadcast.
	Admins  map[string]bool
	Listing Listing
	Log     *slog.Logger
	// RejoinAddress (host:port) is where a player is transferred to join again when a server's
	// content changed during their join or move, so they play with the new content; "" asks them
	// to join again instead.
	RejoinAddress string

	// live is what Reload can change while players are on: Servers, Try, ForcedHosts, Admins.
	live atomic.Pointer[liveConfig]

	mu       sync.Mutex
	content  map[string]*serverContent
	packs    map[string]*cachedPack // uuid_version -> pack
	sessions sync.Map               // xuid -> *switchSession
	fetching map[string]bool
	// requestMode: servers seen sending chunks in sub-chunk request mode (server name -> true).
	// Geyser never does, Dragonfly does; a client must not request sub-chunks from a server that
	// doesn't use them (Geyser kicks it: "unused packet SubChunkRequestPacket").
	requestMode sync.Map
}

// usesRequestMode reports whether a server was seen sending chunks in sub-chunk request mode.
func (n *Network) usesRequestMode(server string) bool {
	v, ok := n.requestMode.Load(server)
	return ok && v.(bool)
}

// Listing is the Friends-tab listing switch (the broadcast), owned by the main package.
type Listing interface {
	On() bool
	Set(on bool) error
	// Worlds is every world's name, the main one first; WorldOn and SetWorld are one world's.
	Worlds() []string
	WorldOn(name string) bool
	SetWorld(name string, on bool) error
}

// ServerConfig is one server of a Network.
type ServerConfig struct {
	Name string `json:"name"`
	// Address is a NetherNet signaling URL (http://127.0.0.1:19133) or, with Transport "raknet", a
	// RakNet host:port.
	Address   string `json:"address"`
	Transport string `json:"transport,omitempty"`
	// Protocols lists the client protocol versions the server takes, for example [2193] for one
	// still on 1.26.50. Empty: every version the relay serves (proxy.SupportedProtocols).
	Protocols []int32 `json:"protocols,omitempty"`
	// Native lists the newer versions the server speaks itself. A player of any other version the
	// server takes logs in with the relay's own and the relay translates their packets,
	// so a server takes players of a version it does not speak.
	Native []int32 `json:"native,omitempty"`
	// NoPacks keeps the server's resource packs out of what players get (resource-packs = false in
	// beacon.toml): its blocks and items still join the union, drawn by the other servers' packs.
	NoPacks bool `json:"no_packs,omitempty"`
	// Forwarding "endstone" (ForwardingEndstone) signs logins to the server for Endstone 0.12's
	// [proxies.endgate], which gives it the player's real XUID and address with no plugin.
	Forwarding string `json:"forwarding,omitempty"`
	// NoEncryption and NoCompression (encryption = false, compression = false in beacon.toml) ask a
	// server reached over TCP not to encrypt or compress the link: on one that never leaves the machine
	// both only cost CPU on each end. Only Dragonfly's TCP listener (through gophertunnel) grants them.
	NoEncryption  bool `json:"no_encryption,omitempty"`
	NoCompression bool `json:"no_compression,omitempty"`
}

// backendProtocol is the version a player of protocol id logs in to the server with.
func (s ServerConfig) backendProtocol(id int32) int32 {
	if slices.Contains(s.Native, id) {
		return id
	}
	return protocol.CurrentProtocol
}

// Accepts reports whether the server takes players of protocol version id.
func (s ServerConfig) Accepts(id int32) bool {
	return proxy.Supported(id) && (len(s.Protocols) == 0 || slices.Contains(s.Protocols, id))
}

// versionMessage tells a player of protocol id that the server does not take their version.
func (s ServerConfig) versionMessage(id int32) string {
	name := s.Name
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	return fmt.Sprintf("%s doesn't support %s yet.", name, proxy.VersionName(id))
}

// probeProtocol is the version the relay's own probes log in with: its own, unless the server
// does not take it.
func (s ServerConfig) probeProtocol() int32 {
	if s.Accepts(protocol.CurrentProtocol) {
		return protocol.CurrentProtocol
	}
	for _, id := range s.Protocols {
		if proxy.Supported(id) {
			return id
		}
	}
	return protocol.CurrentProtocol
}

// serverFor is the server a player of protocol id joins: the default one, or else the first
// other one that takes their version.
func (n *Network) serverFor(id int32) (ServerConfig, bool) {
	for _, s := range n.orderedServers() {
		if s.Accepts(id) {
			return s, true
		}
	}
	return ServerConfig{}, false
}

// serverContent is what a server sent at its last join: what the union is built from.
type serverContent struct {
	StartGame *packet.StartGame
	Items     *packet.ItemRegistry
	Actors    *packet.AvailableActorIdentifiers
	Biomes    *packet.BiomeDefinitionList
	PacksInfo *packet.ResourcePacksInfo
	PackStack *packet.ResourcePackStack
}

type cachedPack struct {
	info protocol.TexturePackInfo
	data []byte
	hash []byte
}

// contentFiles names the cached packets of a server, in Dir/<server>/.
var contentFiles = map[uint32]string{
	packet.IDStartGame:                 "start_game.bin",
	packet.IDItemRegistry:              "item_registry.bin",
	packet.IDAvailableActorIdentifiers: "actor_identifiers.bin",
	packet.IDBiomeDefinitionList:       "biome_definitions.bin",
	packet.IDResourcePacksInfo:         "resource_packs_info.bin",
	packet.IDResourcePackStack:         "resource_pack_stack.bin",
}

// Init loads the cache. Call once before serving.
func (n *Network) Init() error {
	if n.Log == nil {
		n.Log = slog.Default()
	}
	n.content = map[string]*serverContent{}
	n.packs = map[string]*cachedPack{}
	n.fetching = map[string]bool{}
	try := n.Try
	if len(try) == 0 {
		try = []string{n.Default}
	}
	if err := n.setLive(n.Servers, try, n.ForcedHosts, n.Admins); err != nil {
		return err
	}
	for _, s := range n.Servers {
		c := &serverContent{}
		for id, file := range contentFiles {
			b, err := os.ReadFile(filepath.Join(n.Dir, s.Name, file))
			if err != nil {
				continue
			}
			c.set(id, b)
		}
		n.content[s.Name] = c
	}
	entries, _ := os.ReadDir(filepath.Join(n.Dir, "packs"))
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".zip")
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(n.Dir, "packs", e.Name()))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		n.packs[key] = &cachedPack{data: data, hash: sum[:]}
	}
	return nil
}

func (n *Network) server(name string) *ServerConfig {
	return n.conf().server(name)
}

// liveConfig is the part of the network Reload can change. It is replaced whole, never modified.
type liveConfig struct {
	servers []ServerConfig
	try     []string
	forced  map[string][]string // lower-case host -> join order
	admins  map[string]bool
}

func (c *liveConfig) server(name string) *ServerConfig {
	for i := range c.servers {
		if strings.EqualFold(c.servers[i].Name, name) {
			return &c.servers[i]
		}
	}
	return nil
}

func (n *Network) conf() *liveConfig {
	if c := n.live.Load(); c != nil {
		return c
	}
	// Before Init (as in tests): the fields as they are.
	try := n.Try
	if len(try) == 0 && n.Default != "" {
		try = []string{n.Default}
	}
	if len(try) == 0 && len(n.Servers) > 0 {
		try = []string{n.Servers[0].Name}
	}
	n.live.CompareAndSwap(nil, &liveConfig{servers: n.Servers, try: try, forced: map[string][]string{}, admins: n.Admins})
	return n.live.Load()
}

func (n *Network) setLive(servers []ServerConfig, try []string, forced map[string][]string, admins map[string]bool) error {
	c := &liveConfig{servers: servers, try: try, forced: map[string][]string{}, admins: admins}
	if len(try) == 0 {
		return errors.New("try names no server")
	}
	for _, name := range try {
		if c.server(name) == nil {
			return fmt.Errorf("try names %q, which is not a server", name)
		}
	}
	for host, order := range forced {
		for _, name := range order {
			if c.server(name) == nil {
				return fmt.Errorf("forced host %q names %q, which is not a server", host, name)
			}
		}
		c.forced[strings.ToLower(host)] = order
	}
	n.live.Store(c)
	return nil
}

// DefaultServer is the first server players join.
func (n *Network) DefaultServer() string { return n.conf().try[0] }

// IsAdmin reports whether a gamertag (any case) or XUID is one of the admins.
func (n *Network) IsAdmin(xuid, name string) bool {
	// By XUID only: a gamertag can be renamed, and taken by someone else.
	if isJavaKey(xuid) {
		return n.moderation().javaRole(xuid) == "admin" // a Java proxy's admin (moderation_java.go)
	}
	return xuid != "" && (n.conf().admins[xuid] || n.moderation().isExtraAdmin(xuid))
}

// configAdmins lists the admins of beacon.toml.
func (n *Network) configAdmins() []string {
	var out []string
	for a := range n.conf().admins {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// joinOrder is the servers a player of protocol id tries to join, in order: the forced host's
// order when the address they typed (typed, as the client sends it) has one, else Try. Servers that
// don't take their version are left out; if none is left, any server that does.
func (n *Network) joinOrder(id int32, typed string) []ServerConfig {
	c := n.conf()
	order := c.try
	if host := hostOf(typed); host != "" {
		if o, ok := c.forced[host]; ok {
			order = o
		}
	}
	var out []ServerConfig
	for _, name := range order {
		if s := c.server(name); s != nil && s.Accepts(id) {
			out = append(out, *s)
		}
	}
	if len(out) == 0 {
		if s, ok := n.serverFor(id); ok {
			out = append(out, s)
		}
	}
	return out
}

// hostOf is the lower-case host name in an address a client sends ("https://play.example.com:19132").
func hostOf(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.Index(addr, "://"); i >= 0 {
		addr = addr[i+3:]
	}
	addr, _, _ = strings.Cut(addr, "/")
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	return strings.ToLower(strings.Trim(addr, "[]"))
}

// Reload replaces the servers, join order, forced hosts and admins while players are on. Players
// stay where they are; joins and /server use the new lists. A new server's content is learned in
// the background, as at start.
func (n *Network) Reload(servers []ServerConfig, try []string, forced map[string][]string, admins map[string]bool) error {
	if err := n.setLive(servers, try, forced, admins); err != nil {
		return err
	}
	for _, s := range servers {
		n.mu.Lock()
		_, known := n.content[s.Name]
		if !known {
			n.content[s.Name] = &serverContent{}
			n.contentGen++
		}
		n.mu.Unlock()
		if !known {
			go func(name string) {
				if err := n.learn(name); err != nil {
					n.Log.Warn("network: could not learn a reloaded server's content yet", "server", name, "err", err)
				}
			}(s.Name)
		}
	}
	return nil
}

// set decodes and keeps a cached packet payload.
func (c *serverContent) set(id uint32, payload []byte) {
	var pk packet.Packet
	switch id {
	case packet.IDStartGame:
		pk = &packet.StartGame{}
	case packet.IDItemRegistry:
		pk = &packet.ItemRegistry{}
	case packet.IDAvailableActorIdentifiers:
		pk = &packet.AvailableActorIdentifiers{}
	case packet.IDBiomeDefinitionList:
		pk = &packet.BiomeDefinitionList{}
	case packet.IDResourcePacksInfo:
		pk = &packet.ResourcePacksInfo{}
	case packet.IDResourcePackStack:
		pk = &packet.ResourcePackStack{}
	default:
		return
	}
	if decodeInto(pk, payload) != nil {
		return
	}
	switch pk := pk.(type) {
	case *packet.StartGame:
		c.StartGame = pk
	case *packet.ItemRegistry:
		c.Items = pk
	case *packet.AvailableActorIdentifiers:
		c.Actors = pk
	case *packet.BiomeDefinitionList:
		c.Biomes = pk
	case *packet.ResourcePacksInfo:
		c.PacksInfo = pk
	case *packet.ResourcePackStack:
		c.PackStack = pk
	}
}

// record keeps what a server just sent at a join, on disk too when it changed, and starts fetching
// resource packs the relay does not have yet. It reports whether the content changed.
func (n *Network) record(server string, id uint32, payload []byte) bool {
	file, ok := contentFiles[id]
	if !ok {
		return false
	}
	path := filepath.Join(n.Dir, server, file)
	if n.unchanged(server+"/"+file, path, id, payload) {
		return false
	}
	n.mu.Lock()
	c := n.content[server]
	if c == nil {
		c = &serverContent{}
		n.content[server] = c
	}
	c.set(id, payload)
	n.contentGen++
	n.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		n.Log.Warn("network: could not cache server content", "server", server, "file", file, "err", err)
	} else {
		n.Log.Debug("network: server content changed, cached for the next joins", "server", server, "file", file)
	}
	if id == packet.IDResourcePacksInfo || id == packet.IDResourcePackStack {
		n.packGen.Add(1)
	}
	if id == packet.IDResourcePacksInfo {
		go n.fetchPacks(server)
	}
	return true
}

// union is the content a joining client gets: every server's, the default server's first.
type union struct {
	packsInfo *packet.ResourcePacksInfo
	packStack *packet.ResourcePackStack
	blocks    []protocol.BlockEntry
	palette   *blockPalette
	items     *packet.ItemRegistry
	actors    *packet.AvailableActorIdentifiers
	exps      []protocol.ExperimentData
	// rewind is the largest movement rewind history any server asks for: every client gets it at
	// its first join, so a server needing one (BDS) works after a move from one that does not.
	rewind  int32
	missing []string // servers with nothing cached yet
}

func (n *Network) orderedServers() []ServerConfig {
	c := n.conf()
	out := []ServerConfig{*c.server(c.try[0])}
	for _, s := range c.servers {
		if !strings.EqualFold(s.Name, c.try[0]) {
			out = append(out, s)
		}
	}
	return out
}

// seenContent is a cached server packet: the hash of its bytes and its fingerprint.
type seenContent struct {
	sum   [32]byte
	print string
}

// unchanged reports whether payload holds the content cached at path. The last content is kept in
// memory, so a join with the same bytes costs a hash, and one with the same content in another
// order (NBT maps) one decode.
func (n *Network) unchanged(key, path string, id uint32, payload []byte) bool {
	sum := sha256.Sum256(payload)
	n.mu.Lock()
	seen, ok := n.seen[key]
	n.mu.Unlock()
	if ok && seen.sum == sum {
		return true
	}
	if !ok {
		old, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		seen = seenContent{sum: sha256.Sum256(old), print: fingerprint(id, old)}
		if seen.sum == sum {
			n.setSeen(key, seen)
			return true
		}
	}
	print := fingerprint(id, payload)
	n.setSeen(key, seenContent{sum: sum, print: print})
	return print == seen.print
}

func (n *Network) setSeen(key string, c seenContent) {
	n.mu.Lock()
	if n.seen == nil {
		n.seen = map[string]seenContent{}
	}
	n.seen[key] = c
	n.mu.Unlock()
}

// paletteKey names a server's block list and the union palette its map goes to.
type paletteKey struct {
	blocks uint64
	union  *blockPalette
}

type serverPalette struct {
	bp *blockPalette
	bm *blockMap // nil when the palettes are the same
}

// serverPalette returns the palette of a server's StartGame block list and its map to the union
// palette, built once for every player with the same blocks and union (both are only read).
func (n *Network) serverPalette(blocks []protocol.BlockEntry, union *blockPalette) (*blockPalette, *blockMap, error) {
	key := paletteKey{blocksKey(blocks), union}
	n.mu.Lock()
	p, ok := n.palettes[key]
	n.mu.Unlock()
	if ok {
		return p.bp, p.bm, nil
	}
	bp, err := newBlockPalette(blocks)
	if err != nil {
		return nil, nil, err
	}
	var bm *blockMap
	if !bp.sameAs(union) {
		bm = newBlockMap(bp, union)
	}
	n.mu.Lock()
	if n.palettes == nil || len(n.palettes) >= 32 {
		n.palettes = map[paletteKey]serverPalette{}
	}
	n.palettes[key] = serverPalette{bp, bm}
	n.mu.Unlock()
	return bp, bm, nil
}

// blocksKey hashes a block list, names and properties (fmt prints maps sorted).
func blocksKey(blocks []protocol.BlockEntry) uint64 {
	h := fnv.New64a()
	for _, b := range blocks {
		fmt.Fprintf(h, "%s\x00%v\x00", b.Name, b.Properties)
	}
	return h.Sum64()
}

// unionKey is what a built union depends on.
type unionKey struct {
	gen  uint64
	conf *liveConfig
}

// buildUnion returns the union, built again only when the servers' content, the packs or the
// server list changed: every player shares one (it is only read).
func (n *Network) buildUnion() (*union, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	key := unionKey{n.contentGen, n.conf()}
	if n.unionCache != nil && n.unionKey == key {
		return n.unionCache, nil
	}
	u, err := n.makeUnion()
	if err == nil {
		n.unionCache, n.unionKey = u, key
	}
	return u, err
}

// makeUnion builds the union. n.mu held.
func (n *Network) makeUnion() (*union, error) {
	u := &union{
		packsInfo: &packet.ResourcePacksInfo{},
		packStack: &packet.ResourcePackStack{BaseGameVersion: "*"},
		items:     &packet.ItemRegistry{},
	}
	seenPack := map[string]bool{}
	seenStack := map[string]bool{}
	seenBlock := map[string]bool{}
	seenExp := map[string]bool{}
	itemByName := map[string]bool{}
	itemByID := map[int16]string{}
	// blocklessItem is where an item sits in u.items that its server sent without a block of the
	// same name (pvp's geyser_custom:player_skull_* heads).
	blocklessItem := map[string]int{}
	actorSeen := map[string]bool{}
	var actors []any
	// nextItemID is the first number no server uses: an item whose number another server's item
	// already has gets one, and players' items are renumbered by name per server.
	var nextItemID int16
	for _, s := range n.orderedServers() {
		if c := n.content[s.Name]; c != nil && c.Items != nil {
			for _, it := range c.Items.Items {
				if it.RuntimeID >= nextItemID {
					nextItemID = it.RuntimeID + 1
				}
			}
		}
	}
	for i, s := range n.orderedServers() {
		c := n.content[s.Name]
		if c == nil || c.StartGame == nil {
			u.missing = append(u.missing, s.Name)
			continue
		}
		if r := c.StartGame.PlayerMovementSettings.RewindHistorySize; r > u.rewind {
			u.rewind = r
		}
		packs := !s.NoPacks
		if p := c.PacksInfo; p != nil && packs {
			u.packsInfo.TexturePackRequired = u.packsInfo.TexturePackRequired || p.TexturePackRequired
			u.packsInfo.HasScripts = u.packsInfo.HasScripts || p.HasScripts
			u.packsInfo.HasAddons = u.packsInfo.HasAddons || p.HasAddons
			for _, t := range p.TexturePacks {
				key := t.UUID.String() + "_" + t.Version
				if seenPack[t.UUID.String()] {
					continue
				}
				if n.packs[key] == nil {
					n.Log.Warn("network: a resource pack is not downloaded yet, players joining now go without it", "server", s.Name, "pack", key)
					continue
				}
				seenPack[t.UUID.String()] = true
				t.DownloadURL = ""
				u.packsInfo.TexturePacks = append(u.packsInfo.TexturePacks, t)
			}
		}
		if c.PackStack == nil && c.PacksInfo != nil && packs {
			// The stack (which downloaded packs the client applies) isn't known yet, for example
			// after the cache was cleared: apply every pack the server lists, or the client
			// downloads them and shows none.
			for _, t := range c.PacksInfo.TexturePacks {
				id := t.UUID.String()
				if !seenStack[id] && seenPack[id] {
					seenStack[id] = true
					u.packStack.TexturePacks = append(u.packStack.TexturePacks, protocol.StackResourcePack{UUID: id, Version: t.Version})
				}
			}
		}
		if st := c.PackStack; st != nil && packs {
			if i == 0 && st.BaseGameVersion != "" {
				u.packStack.BaseGameVersion = st.BaseGameVersion
			}
			for _, t := range st.TexturePacks {
				if !seenStack[t.UUID] && (seenPack[t.UUID] || !isServerPack(c, t.UUID)) {
					seenStack[t.UUID] = true
					u.packStack.TexturePacks = append(u.packStack.TexturePacks, t)
				}
			}
		}
		for _, b := range c.StartGame.Blocks {
			if !seenBlock[b.Name] {
				seenBlock[b.Name] = true
				u.blocks = append(u.blocks, b)
			}
		}
		for _, e := range c.StartGame.Experiments {
			if !seenExp[e.Name] && e.Enabled {
				seenExp[e.Name] = true
				u.exps = append(u.exps, e)
			}
		}
		if c.Items != nil {
			for _, it := range c.Items.Items {
				if itemByName[it.Name] {
					if j, ok := blocklessItem[it.Name]; ok && seenBlock[it.Name] {
						// A later server has the block too: its item is the block's own (drawn
						// and worn as the block, as Geyser's player skulls are), so the client
						// gets that definition under the first server's number.
						e := &u.items.Items[j]
						e.ComponentBased, e.Version, e.Data = it.ComponentBased, it.Version, it.Data
						delete(blocklessItem, it.Name)
					}
					continue
				}
				if other, ok := itemByID[it.RuntimeID]; ok {
					n.Log.Info("network: two servers number different items the same, this one gets a free number",
						"server", s.Name, "item", it.Name, "id", it.RuntimeID, "other", other, "now", nextItemID)
					it.RuntimeID = nextItemID
					nextItemID++
				}
				itemByName[it.Name], itemByID[it.RuntimeID] = true, it.Name
				if seenBlock[it.Name] && !it.ComponentBased {
					// A custom block's own item: drawn as the block, as Geyser sends it, so the
					// 2D icon Dragonfly gives the Ores Plus ones (128 px pre-renders) goes. Its
					// display name stays: the pack only names these blocks by their addon ids.
					it.Data = withoutItemIcon(it.Data)
				}
				if !seenBlock[it.Name] {
					blocklessItem[it.Name] = len(u.items.Items)
				}
				u.items.Items = append(u.items.Items, it)
			}
		}
		if c.Actors != nil {
			var m map[string]any
			if nbt.NewDecoderWithEncoding(bytes.NewReader(c.Actors.SerialisedEntityIdentifiers), nbt.NetworkLittleEndian).Decode(&m) == nil {
				list, _ := m["idlist"].([]any)
				for _, e := range list {
					em, _ := e.(map[string]any)
					id, _ := em["id"].(string)
					if id != "" && !actorSeen[id] {
						actorSeen[id] = true
						actors = append(actors, e)
					}
				}
			}
		}
	}
	if len(u.missing) == len(n.conf().servers) {
		return nil, errors.New("no server has been joined yet: run backendprobe on each server once")
	}
	u.packStack.TexturePackRequired = u.packsInfo.TexturePackRequired
	u.packStack.Experiments = u.exps
	u.packStack.ExperimentsPreviouslyToggled = len(u.exps) > 0
	b, err := nbtEncode(map[string]any{"idlist": actors})
	if err != nil {
		return nil, err
	}
	u.actors = &packet.AvailableActorIdentifiers{SerialisedEntityIdentifiers: b}
	u.blocks = uniqueBlockIDs(u.blocks) // see blockids.go
	if u.palette, err = newBlockPalette(u.blocks); err != nil {
		return nil, err
	}
	return u, nil
}

// isServerPack reports whether a stack entry is one of the server's own packs (as opposed to a
// vanilla pack the stack names).
func isServerPack(c *serverContent, id string) bool {
	if c.PacksInfo == nil {
		return false
	}
	for _, t := range c.PacksInfo.TexturePacks {
		if t.UUID.String() == id {
			return true
		}
	}
	return false
}

func nbtEncode(v any) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	err := nbt.NewEncoderWithEncoding(buf, nbt.NetworkLittleEndian).Encode(v)
	return buf.Bytes(), err
}

// dialServer opens a backend leg to a server and logs the player into it with the relay's re-signed
// copy of their identity. What follows (packs, StartGame) is the link's to handle.
//
// token is the player's own login token, which a forwarding = "endstone" server gets re-signed; ""
// for Beacon's made-up probe players.
func (n *Network) dialServer(ctx context.Context, s ServerConfig, protocolID int32, id login.IdentityData, cd login.ClientData, playerAddr, token string) (*proxy.ProxyConn, backendConn, error) {
	cfg := Config{BackendTransport: s.Transport, NetherNetIdentity: n.Identity, Log: n.Log}
	var endstone *proxy.EndstoneForwarding
	if s.Forwarding == ForwardingEndstone {
		var err error
		if endstone, cfg.NetherNetIdentity, err = n.endstoneForwarding(ctx, token, playerAddr); err != nil {
			return nil, nil, err
		}
	}
	switch strings.ToLower(s.Transport) {
	case TransportRakNet, TransportTCP:
		cfg.BackendTransport = strings.ToLower(s.Transport)
		cfg.ServerAddress = s.Address
		cfg.BackendNoEncryption, cfg.BackendNoCompression = s.NoEncryption, s.NoCompression
	default:
		cfg.BackendTransport = TransportNetherNet
		cfg.NetherNetBackendAddress = s.Address
	}
	conn, key, err := dialBackend(ctx, cfg, n.Log)
	if err != nil {
		return nil, nil, err
	}
	c := proxy.NewProxyConn(conn, false)
	if endstone != nil {
		c.SetEndstoneForwarding(endstone)
	} else if n.ForwardingSecret != nil {
		c.SetForwarding(&proxy.Forwarding{Secret: n.ForwardingSecret, Server: s.Name, PlayerAddr: playerAddr})
	}
	done := make(chan error, 1)
	go func() { done <- c.ForwardIdentity(protocolID, id, cd, key) }()
	select {
	case err = <-done:
	case <-time.After(backendDialTimeout):
		err = errors.New("login timed out")
	}
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("log in: %w", err)
	}
	return c, conn, nil
}

// fetchPacks downloads the resource packs of a server the relay has not got yet, logging in as a
// made-up player who never gets past the pack screen (so never joins the world).
func (n *Network) fetchPacks(server string) {
	n.mu.Lock()
	if n.fetching[server] {
		n.mu.Unlock()
		return
	}
	n.fetching[server] = true
	c := n.content[server]
	var want []protocol.TexturePackInfo
	if c != nil && c.PacksInfo != nil {
		for _, t := range c.PacksInfo.TexturePacks {
			if n.packs[t.UUID.String()+"_"+t.Version] == nil {
				want = append(want, t)
			}
		}
	}
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.fetching, server)
		n.mu.Unlock()
	}()
	if c != nil && c.PacksInfo != nil && len(want) == 0 {
		return
	}
	if _, err := n.downloadPacks(server); err != nil {
		n.Log.Warn("network: could not download resource packs", "server", server, "err", err)
	}
}

// refresh reads a server's pack list at the pack screen, without joining its world, and downloads
// any new pack. A changed list means the rest of its content may have changed too, so that is
// learned again as well: the next player then gets the new packs at login, instead of finding
// out on reaching the server and having to join again.
func (n *Network) refresh(server string) error {
	n.mu.Lock()
	if n.fetching[server] {
		n.mu.Unlock()
		return nil
	}
	n.fetching[server] = true
	n.mu.Unlock()
	changed, err := n.downloadPacks(server)
	n.mu.Lock()
	delete(n.fetching, server)
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if changed {
		n.Log.Info("network: a server has new resource packs", "server", server)
		// The server may still be closing the pack check's connection: give it a moment.
		for range 3 {
			time.Sleep(3 * time.Second)
			if err = n.learn(server); err == nil {
				return nil
			}
		}
		n.Log.Warn("network: could not learn a server's content (retried at the next join)", "server", server, "err", err)
	}
	return nil
}

const (
	// serverWatchInterval is how often the servers are checked for being up.
	serverWatchInterval = time.Second
	// serverRetryInterval is the pause between tries at a server still starting.
	serverRetryInterval = 5 * time.Second
	// serverRefreshInterval is how often pack lists are read anyway, for a restart too quick to
	// be seen or packs changed without one.
	serverRefreshInterval = 10 * time.Minute
)

// watchServers refreshes a server that comes back up, which is when its packs change, and every
// server now and then. It runs for as long as the relay does.
func (n *Network) watchServers() {
	up := map[string]bool{}
	lastRefresh := time.Now()
	for range time.Tick(serverWatchInterval) {
		periodic := time.Since(lastRefresh) >= serverRefreshInterval
		if periodic {
			lastRefresh = time.Now()
		}
		for _, s := range n.conf().servers {
			now := serverUp(s)
			was, seen := up[s.Name]
			up[s.Name] = now
			if now && (periodic || seen && !was) {
				go n.refreshRetrying(s.Name)
			}
		}
	}
}

// refreshRetrying refreshes a server, trying again for a server still starting.
func (n *Network) refreshRetrying(server string) {
	var err error
	for range 6 {
		if err = n.refresh(server); err == nil {
			return
		}
		time.Sleep(serverRetryInterval)
	}
	n.Log.Warn("network: could not read a server's resource packs", "server", server, "err", err)
}

// serverUp reports whether a server answers: a ping for RakNet, its signaling port for NetherNet.
func serverUp(s ServerConfig) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if GameTransport(s.Transport) {
		_, err := pingGameServer(ctx, s.Transport, s.Address)
		return err == nil
	}
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s.Address, "http://"), "https://"), "/")
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// FetchAllPacks reads every server's pack list and downloads its missing packs, joins each server
// the relay has nothing cached for once, to learn its content, and starts watching the servers;
// run at start.
func (n *Network) FetchAllPacks() {
	go n.watchServers()
	for _, s := range n.conf().servers {
		n.mu.Lock()
		c := n.content[s.Name]
		known := c != nil && c.StartGame != nil
		n.mu.Unlock()
		if known {
			go n.refreshRetrying(s.Name)
			continue
		}
		go func(name string) {
			if err := n.learn(name); err != nil {
				n.Log.Warn("network: could not learn a server's content (retried at the next start or join)", "server", name, "err", err)
			}
		}(name(s))
	}
}

func name(s ServerConfig) string { return s.Name }

// learn joins a server once as a made-up player ("beaconprobe") and caches what it sends, for a server
// nobody has joined through this relay yet. The made-up player is in the world for a few seconds.
func (n *Network) learn(server string) error {
	s := n.server(server)
	if s == nil {
		return fmt.Errorf("no server %q", server)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	id := login.IdentityData{XUID: learnerXUID, DisplayName: "beaconprobe", Identity: "6e327061-636b-3300-8000-000000000003"}
	c, conn, err := n.dialServer(ctx, *s, s.probeProtocol(), id, ProbeClientData("beaconprobe"), "", "")
	if err != nil {
		return err
	}
	defer conn.Close()
	sp, err := joinBackend(c, 4, 30*time.Second)
	if err != nil {
		return err
	}
	_ = c.WritePacket(&packet.SetLocalPlayerAsInitialised{EntityRuntimeID: sp.StartGame.EntityRuntimeID})
	for _, pk := range []packet.Packet{sp.PacksInfo, sp.PackStack, sp.StartGame, sp.ItemRegistry, sp.Actors, sp.Biomes} {
		if reflectNil(pk) {
			continue
		}
		if _, payload, err := splitPacket(encodePacket(pk)); err == nil {
			n.record(server, pk.ID(), payload)
		}
	}
	// Some servers send their registries after the spawn. Closing ends the wait: a read deadline
	// would not, as NetherNet connections ignore it, and the made-up player would stay in the world.
	leave := time.AfterFunc(4*time.Second, func() { _ = conn.Close() })
	defer leave.Stop()
	for {
		pks, err := c.ReadPackets()
		if err != nil {
			break
		}
		for _, raw := range pks {
			if pid, payload, err := splitPacket(raw); err == nil {
				if _, ok := contentFiles[pid]; ok && !(pid == packet.IDItemRegistry && sp.ItemRegistry != nil) {
					// (A second item list after the spawn is an update, not the registry.)
					n.record(server, pid, payload)
				}
			}
		}
	}
	n.Log.Info("network: learned a server's content", "server", server)
	return nil
}

func reflectNil(pk packet.Packet) bool {
	switch pk := pk.(type) {
	case *packet.ResourcePacksInfo:
		return pk == nil
	case *packet.ResourcePackStack:
		return pk == nil
	case *packet.StartGame:
		return pk == nil
	case *packet.ItemRegistry:
		return pk == nil
	case *packet.AvailableActorIdentifiers:
		return pk == nil
	case *packet.BiomeDefinitionList:
		return pk == nil
	}
	return pk == nil
}

// Made-up players the relay logs in as to download packs and learn a server's content. Different
// XUIDs, since both can run at once and servers refuse a second login of one player.
const (
	packFetcherXUID = "2535400000000002"
	learnerXUID     = "2535400000000003"
)

// downloadPacks logs in as far as the pack screen, keeps the server's pack list and downloads the
// packs the relay does not have. It reports whether the list changed.
func (n *Network) downloadPacks(server string) (changed bool, err error) {
	s := n.server(server)
	if s == nil {
		return false, fmt.Errorf("no server %q", server)
	}
	if s.NoPacks {
		return false, nil // resource-packs = false: nothing of its is served
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id := login.IdentityData{XUID: packFetcherXUID, DisplayName: "beaconpacks", Identity: "6e327061-636b-3300-8000-000000000002"}
	c, conn, err := n.dialServer(ctx, *s, s.probeProtocol(), id, ProbeClientData("beaconpacks"), "", "")
	if err != nil {
		return false, err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))

	type pending struct {
		info   *packet.ResourcePackDataInfo
		data   []byte
		chunks uint32
	}
	downloads := map[string]*pending{}
	var info *packet.ResourcePacksInfo
	got := 0
	for {
		pks, err := c.ReadPackets()
		if err != nil {
			return changed, err
		}
		for _, raw := range pks {
			pid, payload, err := splitPacket(raw)
			if err != nil {
				return changed, err
			}
			switch pid {
			case packet.IDDisconnect:
				var pk packet.Disconnect
				_ = decodeInto(&pk, payload)
				return changed, fmt.Errorf("disconnected: %s", pk.Message)
			case packet.IDResourcePacksInfo:
				info = &packet.ResourcePacksInfo{}
				if err := decodeInto(info, payload); err != nil {
					return changed, err
				}
				changed = n.record(server, pid, payload)
				var ids []string
				n.mu.Lock()
				for _, t := range info.TexturePacks {
					key := t.UUID.String() + "_" + t.Version
					if n.packs[key] == nil && t.DownloadURL == "" {
						ids = append(ids, key)
					}
				}
				n.mu.Unlock()
				if len(ids) == 0 {
					return changed, nil
				}
				if err := c.WritePacket(&packet.ResourcePackClientResponse{Response: packet.PackResponseSendPacks, PacksToDownload: ids}); err != nil {
					return changed, err
				}
				got = len(ids)
			case packet.IDResourcePackStack:
				return changed, nil // the server had nothing to send
			case packet.IDResourcePackDataInfo:
				pk := &packet.ResourcePackDataInfo{}
				if err := decodeInto(pk, payload); err != nil {
					return changed, err
				}
				downloads[pk.UUID] = &pending{info: pk, data: make([]byte, 0, pk.Size)}
				for i := uint32(0); i < pk.ChunkCount; i++ {
					if err := c.WritePacket(&packet.ResourcePackChunkRequest{UUID: pk.UUID, ChunkIndex: int32(i)}); err != nil {
						return changed, err
					}
				}
			case packet.IDResourcePackChunkData:
				var pk packet.ResourcePackChunkData
				if err := decodeInto(&pk, payload); err != nil {
					return changed, err
				}
				d := downloads[pk.UUID]
				if d == nil {
					continue
				}
				d.data = append(d.data, pk.Data...)
				d.chunks++
				if d.chunks < d.info.ChunkCount {
					continue
				}
				sum := sha256.Sum256(d.data)
				if len(d.info.Hash) > 0 && !bytes.Equal(sum[:], d.info.Hash) {
					return changed, fmt.Errorf("pack %s: hash mismatch", pk.UUID)
				}
				key := pk.UUID
				if !strings.Contains(key, "_") {
					for _, t := range info.TexturePacks {
						if t.UUID.String() == key {
							key += "_" + t.Version
						}
					}
				}
				_ = os.MkdirAll(filepath.Join(n.Dir, "packs"), 0o755)
				if err := os.WriteFile(filepath.Join(n.Dir, "packs", key+".zip"), d.data, 0o644); err != nil {
					return changed, err
				}
				n.mu.Lock()
				n.packs[key] = &cachedPack{data: d.data, hash: sum[:]}
				n.contentGen++
				n.mu.Unlock()
				n.Log.Info("network: downloaded resource pack", "server", server, "pack", key, "bytes", len(d.data))
				got--
				if got == 0 {
					return changed, nil
				}
			}
		}
	}
}

// packChunkSize is the size of the pack chunks the relay serves.
const packChunkSize = 1024 * 128

func (n *Network) pack(key string) *cachedPack {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.packs[key]
}

// Sessions lists who is on which server.
func (n *Network) Sessions() map[string]string {
	out := map[string]string{}
	n.sessions.Range(func(k, v any) bool {
		out[k.(string)] = v.(*switchSession).serverName()
		return true
	})
	return out
}

// Player is someone connected through the network.
type Player struct{ XUID, Name, Server string }

// Players lists who is connected, by server, then name.
func (n *Network) Players() []Player {
	var out []Player
	n.sessions.Range(func(k, v any) bool {
		s := v.(*switchSession)
		out = append(out, Player{XUID: k.(string), Name: s.id.DisplayName, Server: s.serverName()})
		return true
	})
	slices.SortFunc(out, func(a, b Player) int {
		if c := strings.Compare(a.Server, b.Server); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}

// FindPlayer finds a connected player by gamertag (any case) or XUID.
func (n *Network) FindPlayer(name string) (Player, bool) {
	for _, p := range n.Players() {
		if strings.EqualFold(p.Name, name) || p.XUID == name {
			return p, true
		}
	}
	return Player{}, false
}

// GList describes who is on which server, one line per server, as /glist shows it.
func (n *Network) GList() []string {
	byServer := map[string][]string{}
	players := n.Players()
	for _, p := range players {
		byServer[strings.ToLower(p.Server)] = append(byServer[strings.ToLower(p.Server)], p.Name)
	}
	// Java players, as the Java proxy reports them (javaping.go), on the server of the same name.
	java := javaPlayersOn()
	order := n.ServerNames()
	for _, j := range java {
		key := strings.ToLower(j.server)
		if !slices.ContainsFunc(order, func(s string) bool { return strings.EqualFold(s, j.server) }) {
			order = append(order, j.server) // a server only the Java proxy has
		}
		byServer[key] = append(byServer[key], j.name)
	}
	total := len(players) + len(java)
	head := fmt.Sprintf("%d players online.", total)
	if len(java) > 0 {
		head = fmt.Sprintf("%d players online (%d Bedrock, %d Java).", total, len(players), len(java))
	}
	lines := []string{head}
	for _, name := range order {
		names := byServer[strings.ToLower(name)]
		if len(names) == 0 {
			continue
		}
		slices.SortFunc(names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
		lines = append(lines, fmt.Sprintf("[%s] (%d): %s", name, len(names), strings.Join(names, ", ")))
	}
	return lines
}

// Send moves players to a server: who is a gamertag, "all", or "current:<server>" for everyone on
// that server. It returns how many were moved.
func (n *Network) Send(who, server string) (int, error) {
	if n.server(server) == nil {
		return 0, fmt.Errorf("no server named %q", server)
	}
	var targets []Player
	switch {
	case strings.EqualFold(who, "all"):
		targets = n.Players()
	case strings.HasPrefix(who, "current:"):
		from := strings.TrimPrefix(who, "current:")
		for _, p := range n.Players() {
			if strings.EqualFold(p.Server, from) {
				targets = append(targets, p)
			}
		}
	default:
		p, ok := n.FindPlayer(who)
		if !ok {
			return 0, fmt.Errorf("%s is not online", who)
		}
		targets = []Player{p}
	}
	moved := 0
	for _, p := range targets {
		if strings.EqualFold(p.Server, server) {
			continue
		}
		if err := n.Switch(p.XUID, server); err == nil {
			moved++
		}
	}
	return moved, nil
}

// Switch moves a player (by XUID) to a server.
func (n *Network) Switch(xuid, server string) error {
	v, ok := n.sessions.Load(xuid)
	if !ok {
		return fmt.Errorf("player %s is not connected through this relay", xuid)
	}
	s := n.server(server)
	if s == nil {
		return fmt.Errorf("no server named %q", server)
	}
	return v.(*switchSession).requestSwitch(*s, "")
}

// ServerNames lists the servers.
func (n *Network) ServerNames() []string {
	servers := n.conf().servers
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}

var _ *ecdsa.PrivateKey

// fingerprint is what the union uses of a cached packet, so a server that encodes the same content
// differently each time (NBT maps have no order) does not count as changed.
func fingerprint(id uint32, payload []byte) string {
	c := &serverContent{}
	c.set(id, payload)
	var b strings.Builder
	switch {
	case c.StartGame != nil:
		g := c.StartGame
		fmt.Fprintf(&b, "%d|%d|%+v|%v|%v|", g.EntityRuntimeID, g.EntityUniqueID, g.PlayerMovementSettings, g.ServerAuthoritativeInventory, g.UseBlockNetworkIDHashes)
		for _, bl := range g.Blocks {
			b.WriteString(bl.Name + ",")
		}
		for _, e := range g.Experiments {
			fmt.Fprintf(&b, "%s=%v,", e.Name, e.Enabled)
		}
	case c.Items != nil:
		// In any order: Dragonfly lists its items in a different order at every join.
		items := make([]string, 0, len(c.Items.Items))
		for _, it := range c.Items.Items {
			items = append(items, fmt.Sprintf("%s=%d", it.Name, it.RuntimeID))
		}
		slices.Sort(items)
		b.WriteString(strings.Join(items, ","))
	case c.Actors != nil:
		var m map[string]any
		_ = nbt.NewDecoderWithEncoding(bytes.NewReader(c.Actors.SerialisedEntityIdentifiers), nbt.NetworkLittleEndian).Decode(&m)
		list, _ := m["idlist"].([]any)
		var ids []string
		for _, e := range list {
			em, _ := e.(map[string]any)
			idv, _ := em["id"].(string)
			ids = append(ids, idv)
		}
		slices.Sort(ids)
		b.WriteString(strings.Join(ids, ","))
	case c.Biomes != nil:
		fmt.Fprintf(&b, "%d", len(c.Biomes.BiomeDefinitions))
	case c.PacksInfo != nil:
		// In any order, as the items: Dragonfly lists its packs in a different order at every start.
		packs := make([]string, 0, len(c.PacksInfo.TexturePacks))
		for _, t := range c.PacksInfo.TexturePacks {
			packs = append(packs, fmt.Sprintf("%s_%s", t.UUID, t.Version))
		}
		slices.Sort(packs)
		b.WriteString(strings.Join(packs, ","))
	case c.PackStack != nil:
		packs := make([]string, 0, len(c.PackStack.TexturePacks))
		for _, t := range c.PackStack.TexturePacks {
			packs = append(packs, fmt.Sprintf("%s_%s", t.UUID, t.Version))
		}
		slices.Sort(packs)
		b.WriteString(strings.Join(packs, ","))
	default:
		return string(payload)
	}
	return b.String()
}

// withoutItemIcon copies item component data without item_properties' minecraft:icon.
func withoutItemIcon(data map[string]any) map[string]any {
	comps, ok := data["components"].(map[string]any)
	if !ok {
		return data
	}
	props, ok := comps["item_properties"].(map[string]any)
	if !ok {
		return data
	}
	np := make(map[string]any, len(props))
	for k, v := range props {
		if k != "minecraft:icon" {
			np[k] = v
		}
	}
	nc := make(map[string]any, len(comps))
	for k, v := range comps {
		nc[k] = v
	}
	nc["item_properties"] = np
	out := make(map[string]any, len(data))
	for k, v := range data {
		out[k] = v
	}
	out["components"] = nc
	return out
}

// rejoinTarget is where a player sent to join again for new packs was going, until when.
type rejoinTarget struct {
	server string
	until  time.Time
}

// rejoinWindow is how long the server a player was moving to is kept for their join again.
const rejoinWindow = 2 * time.Minute

// rememberRejoin keeps server as where xuid lands on joining again within rejoinWindow.
func (n *Network) rememberRejoin(xuid, server string) {
	if xuid != "" && server != "" {
		n.rejoinTo.Store(xuid, rejoinTarget{server: server, until: time.Now().Add(rejoinWindow)})
	}
}

// takeRejoin returns, once, the server xuid was moving to when sent to join again, if still fresh.
func (n *Network) takeRejoin(xuid string) string {
	v, ok := n.rejoinTo.LoadAndDelete(xuid)
	if !ok {
		return ""
	}
	t := v.(rejoinTarget)
	if time.Now().After(t.until) {
		return ""
	}
	return t.server
}
