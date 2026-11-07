package bridge

import (
	"strings"
	"sync"
	"time"
)

// A server tells Beacon who is in a fight (the control port's /combat): a player in combat can't
// leave with /server, which would let them escape it. The tag ends by itself.
var (
	combatMu    sync.Mutex
	combatUntil = map[string]time.Time{} // by XUID or Java UUID
)

// SetCombat marks the players with these XUIDs or UUIDs as in combat for d.
func SetCombat(keys []string, d time.Duration) {
	until := time.Now().Add(d)
	combatMu.Lock()
	defer combatMu.Unlock()
	for k, t := range combatUntil {
		if time.Now().After(t) {
			delete(combatUntil, k)
		}
	}
	for _, k := range keys {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			combatUntil[k] = until
		}
	}
}

// CombatLeft is how long the player with this XUID or UUID stays in combat, 0 if not.
func CombatLeft(key string) time.Duration {
	combatMu.Lock()
	defer combatMu.Unlock()
	return max(time.Until(combatUntil[strings.ToLower(strings.TrimSpace(key))]), 0)
}

const combatServerMessage = "<red>You can't change servers while in combat (%ds left).</red>"
