package bridge

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// Java players' pings, as the Java proxy reports them with its Java players every few seconds
// (JavaState), so /ping knows every player of the network, whichever edition.

type javaOnline struct {
	uuid   string
	name   string
	server string
	ms     int64 // -1: not measured yet
	at     time.Time
}

var (
	javaPingsMu sync.Mutex
	javaPings   = map[string]javaOnline{} // by lower-case name
)

// javaPingStale is how long a reported Java player counts as on: a few missed reports.
const javaPingStale = 10 * time.Second

// noteJavaPings keeps the Java players the proxy just reported as the ones on.
func noteJavaPings(players []JavaPlayer) {
	now := time.Now()
	javaPingsMu.Lock()
	defer javaPingsMu.Unlock()
	clear(javaPings)
	for _, p := range players {
		if p.Name != "" {
			javaPings[strings.ToLower(p.Name)] = javaOnline{uuid: p.UUID, name: p.Name, server: p.Server, ms: p.Ping, at: now}
		}
	}
}

// javaPlayersOn are the Java players reported lately.
func javaPlayersOn() []javaOnline {
	javaPingsMu.Lock()
	defer javaPingsMu.Unlock()
	var out []javaOnline
	for _, p := range javaPings {
		if time.Since(p.at) < javaPingStale {
			out = append(out, p)
		}
	}
	return out
}

// pingEntry is one line of /ping: a player of either edition.
type pingEntry struct {
	name string
	d    time.Duration
	ok   bool
}

func (e pingEntry) text() string { return pingText(e.name, e.d, e.ok) }

// pingEntries are everyone on the network, Bedrock and Java, by name.
func (n *Network) pingEntries() []pingEntry {
	var out []pingEntry
	for _, p := range n.Players() {
		d, ok := playerPing(p.XUID)
		out = append(out, pingEntry{p.Name, d, ok})
	}
	for _, j := range javaPlayersOn() {
		out = append(out, pingEntry{j.name, time.Duration(j.ms) * time.Millisecond, j.ms >= 0})
	}
	slices.SortFunc(out, func(a, b pingEntry) int { return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)) })
	return out
}

// findPingEntry finds a player of either edition by name: a Bedrock player first.
func (n *Network) findPingEntry(name string) (pingEntry, bool) {
	if p, ok := n.FindPlayer(name); ok {
		d, ok := playerPing(p.XUID)
		return pingEntry{p.Name, d, ok}, true
	}
	for _, j := range javaPlayersOn() {
		if strings.EqualFold(j.name, name) {
			return pingEntry{j.name, time.Duration(j.ms) * time.Millisecond, j.ms >= 0}, true
		}
	}
	return pingEntry{}, false
}

// RunPing runs /ping for the Java proxy, for a player it has, with args as typed (a name, @a, @r):
// what to say, and false when no player of that name is on. The proxy answers @s and no
// argument itself, with its own measure.
func (n *Network) RunPing(args string) ([]string, bool) {
	var out []string
	s := &switchSession{n: n, reply: func(m string) { out = append(out, m) }}
	if !s.pingCommand(strings.Fields(args)) {
		return nil, false
	}
	return out, true
}

// onlineNames are everyone on the network for command suggestions: Bedrock players, then the Java
// players the Java proxy reported, sorted, each once.
func (n *Network) onlineNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if k := strings.ToLower(name); name != "" && !seen[k] {
			seen[k] = true
			out = append(out, name)
		}
	}
	for _, p := range n.Players() {
		add(p.Name)
	}
	for _, j := range javaPlayersOn() {
		add(j.name)
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}
