package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// A client keeps the resource packs it downloaded. packMemory holds, by XUID, the resource-packs =
// "on-join" servers whose packs a player's client took at a join, with what those packs were then
// (onJoinPrint). A later join gives the player those packs again (the client has them cached, so
// nothing downloads) and going to those servers needs no join again; a server whose packs changed
// since is left out of the join until the player goes there and joins again for the new ones.
type packMemory struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]map[string]string // xuid -> lower-case server -> onJoinPrint
}

func (n *Network) packMemoryPath() string { return filepath.Join(n.Dir, "onjoin-packs.json") }

// packMemoryLoad reads the memory once. packMem.mu held.
func (n *Network) packMemoryLoad() {
	pm := &n.packMem
	if pm.loaded {
		return
	}
	pm.loaded = true
	pm.m = map[string]map[string]string{}
	if n.Dir == "" {
		return
	}
	if b, err := os.ReadFile(n.packMemoryPath()); err == nil {
		if err := json.Unmarshal(b, &pm.m); err != nil {
			n.Log.Warn("network: could not read the on-join pack memory, starting empty", "err", err)
			pm.m = map[string]map[string]string{}
		}
	}
}

// onJoinPrint is what an on-join server's join gives a client: its packs the relay has and its
// custom blocks. "" when it gives nothing.
func (n *Network) onJoinPrint(server string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.content[server]
	if c == nil {
		return ""
	}
	var keys []string
	if c.PacksInfo != nil {
		for _, t := range c.PacksInfo.TexturePacks {
			if key := t.UUID.String() + "_" + t.Version; n.packs[key] != nil {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	var blocks uint64
	if c.StartGame != nil && len(c.StartGame.Blocks) > 0 {
		blocks = blocksKey(c.StartGame.Blocks)
	}
	if len(keys) == 0 && blocks == 0 {
		return ""
	}
	return fmt.Sprintf("%s|%x", strings.Join(keys, ","), blocks)
}

// withKnownPacks is variant (unionVariant of the server a join heads to) with the on-join servers
// whose current packs the player's client already took, and the prints of every on-join server
// the result holds (by lower-case name), for rememberPacks once the client has them.
func (n *Network) withKnownPacks(xuid, variant string) (string, map[string]string) {
	prints := map[string]string{}
	names := variantNames(variant)
	for _, s := range n.orderedServers() {
		if !s.PacksOnJoin || s.NoPacks {
			continue
		}
		low := strings.ToLower(s.Name)
		p := n.onJoinPrint(s.Name)
		if p == "" {
			continue
		}
		if slices.Contains(names, low) {
			prints[low] = p
			continue
		}
		if xuid == "" || !s.Allows(xuid) {
			continue
		}
		n.packMem.mu.Lock()
		n.packMemoryLoad()
		had := n.packMem.m[xuid][low]
		n.packMem.mu.Unlock()
		if had == p {
			names = append(names, low)
			prints[low] = p
		}
	}
	return n.normVariant(strings.Join(names, ",")), prints
}

// rememberPacks notes that the client of xuid took the packs of the on-join servers in prints.
func (n *Network) rememberPacks(xuid string, prints map[string]string) {
	if xuid == "" || len(prints) == 0 {
		return
	}
	pm := &n.packMem
	pm.mu.Lock()
	defer pm.mu.Unlock()
	n.packMemoryLoad()
	mine := pm.m[xuid]
	changed := false
	for server, p := range prints {
		if mine[server] != p {
			if mine == nil {
				mine = map[string]string{}
				pm.m[xuid] = mine
			}
			mine[server] = p
			changed = true
		}
	}
	if !changed || n.Dir == "" {
		return
	}
	b, err := json.Marshal(pm.m)
	if err == nil {
		tmp := n.packMemoryPath() + ".tmp"
		if err = os.WriteFile(tmp, b, 0o644); err == nil {
			err = os.Rename(tmp, n.packMemoryPath())
		}
	}
	if err != nil {
		n.Log.Warn("network: could not save the on-join pack memory", "err", err)
	}
}

// variantNames splits a union variant: the lower-case names of the on-join servers whose packs it
// holds, comma separated ("" for none).
func variantNames(variant string) []string {
	if variant == "" {
		return nil
	}
	return strings.Split(variant, ",")
}

// variantHas reports whether variant holds the packs of the on-join server name.
func variantHas(variant, name string) bool {
	return slices.ContainsFunc(variantNames(variant), func(v string) bool { return strings.EqualFold(v, name) })
}

// normVariant is variant with only on-join servers, lower case, sorted, each once: every join with
// the same packs shares one union.
func (n *Network) normVariant(variant string) string {
	var out []string
	for _, v := range variantNames(variant) {
		if u := n.unionVariant(v); u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

// packsChanged reports whether the packs server gives this client changed since its login: the
// on-join server's own when the client took them, the network's shared ones otherwise.
func (s *switchSession) packsChanged(server string) bool {
	if s.onJoinGens == nil {
		return s.n.packGenFor(s.packsFor) != s.packGen
	}
	if g, ok := s.onJoinGens[strings.ToLower(server)]; ok {
		return s.n.onJoinGenOf(server).Load() != g
	}
	return s.n.packGen.Load() != s.baseGen
}
