package bridge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"fmt"
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
	// server takes logs in with the relay's own (1.26.50) and the relay translates their packets,
	// so a 1.26.50 server takes 1.26.60 players unchanged.
	Native []int32 `json:"native,omitempty"`
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
	a := n.conf().admins
	return a[xuid] || a[strings.ToLower(name)]
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
	if old, err := os.ReadFile(path); err == nil && (bytes.Equal(old, payload) || fingerprint(id, old) == fingerprint(id, payload)) {
		return false
	}
	n.mu.Lock()
	c := n.content[server]
	if c == nil {
		c = &serverContent{}
		n.content[server] = c
	}
	c.set(id, payload)
	n.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		n.Log.Warn("network: could not cache server content", "server", server, "file", file, "err", err)
	} else {
		n.Log.Debug("network: server content changed, cached for the next joins", "server", server, "file", file)
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
	missing   []string // servers with nothing cached yet
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

func (n *Network) buildUnion() (*union, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
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
	for i, s := range n.orderedServers() {
		c := n.content[s.Name]
		if c == nil || c.StartGame == nil {
			u.missing = append(u.missing, s.Name)
			continue
		}
		if p := c.PacksInfo; p != nil {
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
		if c.PackStack == nil && c.PacksInfo != nil {
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
		if st := c.PackStack; st != nil {
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
					n.Log.Warn("network: two servers number different items the same, players switching may see the wrong item",
						"server", s.Name, "item", it.Name, "id", it.RuntimeID, "other", other)
					continue
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
func (n *Network) dialServer(ctx context.Context, s ServerConfig, protocolID int32, id login.IdentityData, cd login.ClientData, playerAddr string) (*proxy.ProxyConn, backendConn, error) {
	cfg := Config{BackendTransport: s.Transport, NetherNetIdentity: n.Identity, Log: n.Log}
	switch strings.ToLower(s.Transport) {
	case TransportRakNet:
		cfg.GeyserAddress = s.Address
	default:
		cfg.BackendTransport = TransportNetherNet
		cfg.NetherNetBackendAddress = s.Address
	}
	conn, key, err := dialBackend(ctx, cfg, n.Log)
	if err != nil {
		return nil, nil, err
	}
	c := proxy.NewProxyConn(conn, false)
	if n.ForwardingSecret != nil {
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
	if err := n.downloadPacks(server); err != nil {
		n.Log.Warn("network: could not download resource packs", "server", server, "err", err)
	}
}

// FetchAllPacks downloads every server's missing packs (and learns their pack lists), and joins
// each server the relay has nothing cached for once, to learn its content; run at start.
func (n *Network) FetchAllPacks() {
	for _, s := range n.conf().servers {
		n.mu.Lock()
		c := n.content[s.Name]
		known := c != nil && c.StartGame != nil
		n.mu.Unlock()
		if known {
			go n.fetchPacks(s.Name)
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
	c, conn, err := n.dialServer(ctx, *s, s.probeProtocol(), id, ProbeClientData("beaconprobe"), "")
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
	// Some servers send their registries after the spawn.
	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
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

func (n *Network) downloadPacks(server string) error {
	s := n.server(server)
	if s == nil {
		return fmt.Errorf("no server %q", server)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id := login.IdentityData{XUID: packFetcherXUID, DisplayName: "beaconpacks", Identity: "6e327061-636b-3300-8000-000000000002"}
	c, conn, err := n.dialServer(ctx, *s, s.probeProtocol(), id, ProbeClientData("beaconpacks"), "")
	if err != nil {
		return err
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
			return err
		}
		for _, raw := range pks {
			pid, payload, err := splitPacket(raw)
			if err != nil {
				return err
			}
			switch pid {
			case packet.IDDisconnect:
				var pk packet.Disconnect
				_ = decodeInto(&pk, payload)
				return fmt.Errorf("disconnected: %s", pk.Message)
			case packet.IDResourcePacksInfo:
				info = &packet.ResourcePacksInfo{}
				if err := decodeInto(info, payload); err != nil {
					return err
				}
				n.record(server, pid, payload)
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
					return nil
				}
				if err := c.WritePacket(&packet.ResourcePackClientResponse{Response: packet.PackResponseSendPacks, PacksToDownload: ids}); err != nil {
					return err
				}
				got = len(ids)
			case packet.IDResourcePackStack:
				return nil // the server had nothing to send
			case packet.IDResourcePackDataInfo:
				pk := &packet.ResourcePackDataInfo{}
				if err := decodeInto(pk, payload); err != nil {
					return err
				}
				downloads[pk.UUID] = &pending{info: pk, data: make([]byte, 0, pk.Size)}
				for i := uint32(0); i < pk.ChunkCount; i++ {
					if err := c.WritePacket(&packet.ResourcePackChunkRequest{UUID: pk.UUID, ChunkIndex: int32(i)}); err != nil {
						return err
					}
				}
			case packet.IDResourcePackChunkData:
				var pk packet.ResourcePackChunkData
				if err := decodeInto(&pk, payload); err != nil {
					return err
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
					return fmt.Errorf("pack %s: hash mismatch", pk.UUID)
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
					return err
				}
				n.mu.Lock()
				n.packs[key] = &cachedPack{data: d.data, hash: sum[:]}
				n.mu.Unlock()
				n.Log.Info("network: downloaded resource pack", "server", server, "pack", key, "bytes", len(d.data))
				got--
				if got == 0 {
					return nil
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
		byServer[p.Server] = append(byServer[p.Server], p.Name)
	}
	lines := []string{fmt.Sprintf("%d players online.", len(players))}
	for _, name := range n.ServerNames() {
		if names := byServer[name]; len(names) > 0 {
			lines = append(lines, fmt.Sprintf("[%s] (%d): %s", name, len(names), strings.Join(names, ", ")))
		}
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
