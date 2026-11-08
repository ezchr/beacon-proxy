package bridge

import (
	"sync"
	"sync/atomic"
	"time"
)

// Rate limits and the server-list passthrough, from beacon.toml; Reload replaces them.

// Limits are the rate limits. Zero turns one off.
type Limits struct {
	// LoginInterval is the least time between two joins from one IP address.
	LoginInterval time.Duration
	// PacketsPerSecond is the most packets a player may send in a second.
	PacketsPerSecond int
	// SessionsPerIP is the most players one IP address may have online at once.
	SessionsPerIP int
}

var limits atomic.Pointer[Limits]

// SetLimits sets the rate limits.
func SetLimits(l Limits) { limits.Store(&l) }

func currentLimits() Limits {
	if l := limits.Load(); l != nil {
		return *l
	}
	return Limits{}
}

const (
	loginTooFastMessage    = "You're joining too fast. Wait a few seconds and try again."
	tooManyPacketsMessage  = "Too many packets."
	tooManySessionsMessage = "Too many players are online from your connection."
)

var lastLogin sync.Map // ip -> time.Time

// loginTooSoon reports whether ip joined less than the login interval ago, and records this join.
func loginTooSoon(ip string) bool {
	interval := currentLimits().LoginInterval
	if interval <= 0 || ip == "" {
		return false
	}
	now := time.Now()
	prev, loaded := lastLogin.Swap(ip, now)
	if loaded && now.Sub(prev.(time.Time)) < interval {
		return true
	}
	if !loaded {
		// Forget addresses once they can join again, so the map doesn't grow without end.
		time.AfterFunc(interval, func() {
			if t, ok := lastLogin.Load(ip); ok && time.Since(t.(time.Time)) >= interval {
				lastLogin.CompareAndDelete(ip, t)
			}
		})
	}
	return false
}

var (
	sessionsMu sync.Mutex
	sessions   = map[string]map[string]int{} // ip -> xuid -> sessions of that player from it
)

// holdSession counts a player's session against their IP address and reports whether it is within
// the cap; release ends it. A player already online from the address (joining again, which replaces
// their old session) is always let in.
func holdSession(ip, xuid string) (release func(), ok bool) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	players := sessions[ip]
	if limit := currentLimits().SessionsPerIP; limit > 0 && players[xuid] == 0 && len(players) >= limit {
		return nil, false
	}
	if players == nil {
		players = map[string]int{}
		sessions[ip] = players
	}
	players[xuid]++
	return func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		if players[xuid]--; players[xuid] <= 0 {
			delete(players, xuid)
		}
		if len(players) == 0 {
			delete(sessions, ip)
		}
	}, true
}

// packetMeter counts one player's packets. The limit is an average over packetWindow, like Velocity's
// and Paper's packet limiters: short bursts (mining with Haste, a full inventory selling at once)
// pass, a steady flood doesn't. A single second past packetBurst times the limit is too many anyway.
// Used by one goroutine only.
type packetMeter struct {
	second  int64             // the Unix second buckets[cur] counts
	cur     int               // the bucket of second
	buckets [packetWindow]int // packets in each of the last seconds
}

const (
	packetWindow = 7 // seconds
	packetBurst  = 4
)

// over adds n packets and reports whether the player is past the limit.
func (m *packetMeter) over(n int) bool {
	limit := currentLimits().PacketsPerSecond
	if limit <= 0 {
		return false
	}
	now := time.Now().Unix()
	if gap := now - m.second; gap > 0 {
		for k := int64(0); k < min(gap, packetWindow); k++ {
			m.cur = (m.cur + 1) % packetWindow
			m.buckets[m.cur] = 0
		}
		m.second = now
	}
	m.buckets[m.cur] += n
	if m.buckets[m.cur] > limit*packetBurst {
		return true
	}
	total := 0
	for _, b := range m.buckets {
		total += b
	}
	return total > limit*packetWindow
}

// Ping passthrough: what the server list shows from the first server's own entry instead of
// Beacon's: "off", "motd" (its name), "players" (its player counts) or "all".
var pingPassthrough atomic.Value

// SetPingPassthrough sets the ping passthrough mode.
func SetPingPassthrough(mode string) { pingPassthrough.Store(mode) }

func passthrough() string {
	m, _ := pingPassthrough.Load().(string)
	return m
}
