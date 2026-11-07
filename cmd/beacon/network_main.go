package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/ezchr/beacon-proxy/internal/bridge"
	"github.com/ezchr/beacon-proxy/internal/session"
)

// network is the relay's server network (config "servers"), or nil for a relay into one server.
var network *bridge.Network

// setupNetwork builds the network from the config. Relay mode only: the relay has to stay in the
// player's connection to move them.
func setupNetwork(cfg FileConfig, authSession *session.Session, log *slog.Logger) (*bridge.Network, error) {
	if len(cfg.Servers) == 0 {
		return nil, nil
	}
	if strings.EqualFold(strings.TrimSpace(cfg.BackendTransport), bridge.TransportNetherNetNoRelay) {
		return nil, errors.New(`"servers" needs backend_transport "nethernet" (relay mode): in no-relay mode the relay is not in the connection, so it can't move players`)
	}
	servers, err := normalizeServers(cfg.Servers)
	if err != nil {
		return nil, err
	}
	def := cfg.DefaultServer
	if def == "" {
		def = cfg.Servers[0].Name
	}
	dir := cfg.NetworkDir
	if dir == "" {
		dir = "network"
	}
	n := &bridge.Network{
		Servers:          servers,
		Default:          def,
		Try:              cfg.Try,
		ForcedHosts:      cfg.ForcedHosts,
		Dir:              dir,
		Identity:         identityCacheFor(authSession, log).Identity,
		Admins:           adminSet(cfg.Admins),
		Log:              log,
		RejoinAddress:    cfg.RejoinAddress,
		ForwardingSecret: cfg.ForwardingSecret,
	}
	if listing != nil {
		n.Listing = listing
	}
	if err := n.Init(); err != nil {
		return nil, err
	}
	n.FetchAllPacks()
	return n, nil
}

// normalizeServers checks the server list and puts NetherNet addresses in the form dialing needs.
func normalizeServers(in []bridge.ServerConfig) ([]bridge.ServerConfig, error) {
	out := slices.Clone(in)
	seen := map[string]bool{}
	for i, s := range out {
		if s.Name == "" || s.Address == "" {
			return nil, fmt.Errorf("servers[%d] needs a name and an address", i)
		}
		if seen[strings.ToLower(s.Name)] {
			return nil, fmt.Errorf("two servers are called %q", s.Name)
		}
		seen[strings.ToLower(s.Name)] = true
		if !strings.EqualFold(s.Transport, bridge.TransportRakNet) {
			addr, err := bridge.NormalizeNetherNetAddress(s.Address)
			if err != nil {
				return nil, fmt.Errorf("server %s: %w", s.Name, err)
			}
			out[i].Address = addr
		}
	}
	return out, nil
}

// listing switches the Friends-tab listing on and off, for every world or one: config
// "listing_toggle". Off, the relay keeps no Xbox Live session, so the world is not in anyone's
// Friends tab; players already in stay, and direct IP joins keep working. The state survives
// restarts (listing_state.json). nil when the toggle is not configured: always listed.
var listing *listingGate

var errListingOff = errors.New("the Friends-tab listing was switched off")

// mainWorld is how players and admins name the primary broadcast in /broadcast and on the control
// port.
const mainWorld = "main"

type listingGate struct {
	mu     sync.Mutex
	off    map[string]bool // worlds switched off, by lower-case name
	worlds []string        // every world's name, main first
	path   string
	// changed is closed and replaced on every switch: waiters re-check their own world.
	changed chan struct{}
	log     *slog.Logger
}

// newListingGate reads the saved state. The old file ({"on": false}) means every world off.
func newListingGate(path string, worlds []string, log *slog.Logger) *listingGate {
	g := &listingGate{path: path, off: map[string]bool{}, worlds: worlds, changed: make(chan struct{}), log: log}
	var st struct {
		On  *bool    `json:"on"`
		Off []string `json:"off"`
	}
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &st) == nil {
		if st.Off != nil {
			for _, w := range st.Off {
				g.off[strings.ToLower(w)] = true
			}
		} else if st.On != nil && !*st.On {
			for _, w := range worlds {
				g.off[strings.ToLower(w)] = true
			}
		}
	}
	return g
}

// worldKey is name as the gate stores it: the primary broadcast is "main".
func worldKey(name string) string {
	if name == primaryBroadcast {
		name = mainWorld
	}
	return strings.ToLower(name)
}

// On reports whether any world is listed.
func (g *listingGate) On() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, w := range g.worlds {
		if !g.off[worldKey(w)] {
			return true
		}
	}
	return false
}

// Set switches every world.
func (g *listingGate) Set(on bool) error {
	return g.set(g.worlds, on)
}

// Worlds is every world's name, main first.
func (g *listingGate) Worlds() []string { return slices.Clone(g.worlds) }

// WorldOn reports whether the world named name is listed.
func (g *listingGate) WorldOn(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.off[worldKey(name)]
}

// SetWorld switches one world. An unknown name is an error.
func (g *listingGate) SetWorld(name string, on bool) error {
	for _, w := range g.worlds {
		if worldKey(w) == worldKey(name) {
			return g.set([]string{w}, on)
		}
	}
	return fmt.Errorf("no world called %q (worlds: %s)", name, strings.Join(g.worlds, ", "))
}

func (g *listingGate) set(worlds []string, on bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := maps.Clone(g.off)
	for _, w := range worlds {
		if on {
			delete(next, worldKey(w))
		} else {
			next[worldKey(w)] = true
		}
	}
	if maps.Equal(next, g.off) {
		return nil
	}
	off := slices.Sorted(maps.Keys(next))
	b, _ := json.Marshal(map[string]any{"off": off})
	if err := os.WriteFile(g.path, b, 0o644); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	g.off = next
	close(g.changed)
	g.changed = make(chan struct{})
	g.log.Info("friends-tab listing switched", "worlds", worlds, "on", on, "off now", off)
	return nil
}

// waitOn blocks until the world named name is listed.
func (g *listingGate) waitOn(ctx context.Context, name string) error {
	for {
		g.mu.Lock()
		on, ch := !g.off[worldKey(name)], g.changed
		g.mu.Unlock()
		if on {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// waitOff blocks until the world named name is switched off.
func (g *listingGate) waitOff(ctx context.Context, name string) error {
	for {
		g.mu.Lock()
		off, ch := g.off[worldKey(name)], g.changed
		g.mu.Unlock()
		if off {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// serveNetworkControl answers the control port's network and listing routes. It reports whether
// the path was one of them.
//
//	/listing[/status] /listing/on /listing/off   the Friends-tab listing (?world=NAME: one world)
//	/servers                                     the servers and who is on which
//	/switch?xuid=X&server=NAME                   move a player
func serveNetworkControl(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	switch {
	case path == "/listing" || path == "/listing/status" || path == "/listing/on" || path == "/listing/off":
		if listing == nil {
			http.Error(w, "the Friends-tab listing toggle is off for this relay (config listing_toggle)", http.StatusNotFound)
			return true
		}
		if path == "/listing/on" || path == "/listing/off" {
			on := path == "/listing/on"
			var err error
			if world := r.URL.Query().Get("world"); world != "" {
				err = listing.SetWorld(world, on)
			} else {
				err = listing.Set(on)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return true
			}
		}
		worlds := map[string]bool{}
		for _, name := range listing.Worlds() {
			worlds[name] = listing.WorldOn(name)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"on": listing.On(), "worlds": worlds})
		return true
	case path == "/close" || path == "/open":
		if network == nil {
			http.Error(w, "this relay has no servers list", http.StatusNotFound)
			return true
		}
		if path == "/open" {
			if err := network.Open(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"closed": false})
			return true
		}
		msg := strings.TrimSpace(r.URL.Query().Get("message"))
		if msg == "" {
			msg = "The server is closed for now. Come back soon!"
		}
		kicked, err := network.Close(msg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"closed": true, "message": msg, "disconnected": kicked})
		return true
	case path == "/servers":
		if network == nil {
			http.Error(w, "this relay has no servers list", http.StatusNotFound)
			return true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"servers": network.ServerNames(), "default": network.DefaultServer(), "players": network.Sessions()})
		return true
	case path == "/switch":
		if network == nil {
			http.Error(w, "this relay has no servers list", http.StatusNotFound)
			return true
		}
		if err := network.Switch(r.URL.Query().Get("xuid"), r.URL.Query().Get("server")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		_, _ = w.Write([]byte("moving\n"))
		return true
	}
	return false
}

// directIPExtra is the control server when it shares the direct-IP door (a bind on 127.0.0.1).
func directIPExtra(cfg FileConfig, control *controlServer) http.Handler {
	if !cfg.ControlOnDirectIP {
		return nil
	}
	return control
}
