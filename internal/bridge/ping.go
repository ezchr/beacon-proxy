package bridge

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// /ping for everyone on a Network, answered by the relay to the player who typed it only:
//
//	/ping              your own ping
//	/ping <player>     a player's ping (gamertag, any case; quotes for names with spaces)
//	/ping @s | @p      your own ping
//	/ping @a | @e      everyone's
//	/ping @r           a random player's
//
// The ping is the player's own connection to the relay, the same number the control API's /ping
// gives. A name that is not one of the relay's players goes on to the server, whose own /ping
// may know them (a Java player behind Velocity).

// playerPing is a connected player's round trip, false when the relay has none for them.
func playerPing(xuid string) (time.Duration, bool) {
	pingRegistryMu.Lock()
	l, ok := pingRegistry[xuid]
	pingRegistryMu.Unlock()
	if !ok || l.client == nil {
		return 0, false
	}
	d := clientRoundTrip(l.client)
	if l.backend != nil {
		d += 2 * l.backend.Latency()
	}
	return d, true
}

// pingText is "<name> <ms> ms", in plain white.
func pingText(name string, d time.Duration, ok bool) string {
	if !ok {
		return name + " (not measured yet)"
	}
	return fmt.Sprintf("%s %d ms", name, d.Milliseconds())
}

// pingCommand runs /ping with args and reports whether the relay answered it.
func (s *switchSession) pingCommand(args []string) bool {
	arg := strings.Trim(strings.TrimSpace(strings.Join(args, " ")), `"`)
	self := Player{XUID: s.id.XUID, Name: s.id.DisplayName}
	if strings.HasPrefix(arg, "@") {
		sel := strings.ToLower(arg)
		if i := strings.IndexByte(sel, '['); i >= 0 {
			sel = sel[:i] // selector arguments are not supported: @a[r=5] is @a
		}
		players := s.n.Players()
		switch sel {
		case "@s", "@p":
			arg = ""
		case "@a", "@e":
			if len(players) == 0 {
				s.message("<grey>Nobody is online.</grey>")
				return true
			}
			slices.SortFunc(players, func(a, b Player) int {
				return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
			})
			parts := make([]string, 0, len(players))
			for _, p := range players {
				d, ok := playerPing(p.XUID)
				parts = append(parts, pingText(p.Name, d, ok))
			}
			s.message("<yellow>Ping:</yellow> %s", strings.Join(parts, "<grey>,</grey> "))
			return true
		case "@r":
			if len(players) == 0 {
				arg = ""
				break
			}
			p := players[rand.IntN(len(players))]
			d, ok := playerPing(p.XUID)
			s.message("<yellow>Ping:</yellow> %s", pingText(p.Name, d, ok))
			return true
		default:
			s.message("<red>Use @s, @p, @a, @e or @r, or a player's name.</red>")
			return true
		}
	}
	if arg == "" {
		d, ok := playerPing(self.XUID)
		s.message("<yellow>Your ping:</yellow> %s", strings.TrimPrefix(pingText("", d, ok), " "))
		return true
	}
	p, ok := s.n.FindPlayer(arg)
	if !ok {
		return false // not one of the relay's players: the server's own /ping may know them
	}
	d, measured := playerPing(p.XUID)
	s.message("<yellow>Ping:</yellow> %s", pingText(p.Name, d, measured))
	return true
}

// pingOverloads are /ping's: online players' names as suggestions, then any target (a typed name
// or an @ selector, which the client suggests), or nothing for your own.
func (s *switchSession) pingOverloads(enum func(string, []string) uint32) []protocol.CommandOverload {
	const valid = protocol.CommandArgValid
	target := protocol.CommandOverload{Parameters: []protocol.CommandParameter{
		{Name: "player", Type: valid | protocol.CommandArgTypeTarget, Optional: true},
	}}
	var names []string
	for _, p := range s.n.Players() {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return []protocol.CommandOverload{target}
	}
	listed := protocol.CommandOverload{Parameters: []protocol.CommandParameter{
		{Name: "player", Type: valid | protocol.CommandArgEnum | enum("BeaconPingPlayer", names)},
	}}
	return []protocol.CommandOverload{listed, target}
}
