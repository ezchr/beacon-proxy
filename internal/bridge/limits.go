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
	loginTooFastMessage   = "You're joining too fast. Wait a few seconds and try again."
	tooManyPacketsMessage = "Too many packets."
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

// packetMeter counts one player's packets per second. Used by one goroutine only.
type packetMeter struct {
	second time.Time
	n      int
}

// over adds n packets and reports whether the player is past the limit for this second.
func (m *packetMeter) over(n int) bool {
	limit := currentLimits().PacketsPerSecond
	if limit <= 0 {
		return false
	}
	now := time.Now()
	if now.Sub(m.second) >= time.Second {
		m.second, m.n = now, 0
	}
	m.n += n
	return m.n > limit
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
