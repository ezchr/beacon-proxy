package bridge

import (
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
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
		d += clientRoundTrip(l.backend) // the server leg, for a server on another machine
	}
	return d, true
}

// pingText is "<name>'s ping: <ms> ms" ("Your ping: ..." for an empty name), in plain white.
func pingText(name string, d time.Duration, ok bool) string {
	who := "Your ping"
	if name != "" {
		who = name + "'s ping"
	}
	if !ok {
		return who + ": not measured yet"
	}
	return fmt.Sprintf("%s: %d ms", who, d.Milliseconds())
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
		players := s.n.pingEntries() // Bedrock and Java players (javaping.go)
		switch sel {
		case "@s", "@p":
			arg = ""
		case "@a", "@e":
			if len(players) == 0 {
				s.message("<grey>Nobody is online.</grey>")
				return true
			}
			for _, p := range players {
				s.message("%s", p.text())
			}
			return true
		case "@r":
			if len(players) == 0 {
				arg = ""
				break
			}
			s.message("%s", players[rand.IntN(len(players))].text())
			return true
		default:
			s.message("<red>Use @s, @p, @a, @e or @r, or a player's name.</red>")
			return true
		}
	}
	if arg == "" {
		d, ok := playerPing(self.XUID)
		s.message("%s", pingText("", d, ok))
		return true
	}
	p, ok := s.n.findPingEntry(arg)
	if !ok {
		return false // no player of that name on: the server's own /ping may know them
	}
	s.message("%s", p.text())
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
	names = append(names, s.n.onlineNames()...)
	if len(names) == 0 {
		return []protocol.CommandOverload{target}
	}
	listed := protocol.CommandOverload{Parameters: []protocol.CommandParameter{
		{Name: "player", Type: valid | protocol.CommandArgEnum | enum("BeaconPingPlayer", names)},
	}}
	return []protocol.CommandOverload{listed, target}
}

// remoteServer reports whether a server's address is on another machine: its leg then counts in
// /ping. On this machine it is next to nothing, and NetherNet's own reading of it is wrong.
func remoteServer(address string) bool {
	host := address
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		host = u.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") || host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback()
	}
	return true
}

// setBackendPing makes /ping count the leg to l's server if it is on another machine.
func (s *switchSession) setBackendPing(l *link) {
	if s.id.XUID == "" {
		return
	}
	if remoteServer(l.server.Address) {
		registerBackendPingIf(s.id.XUID, l.conn)
		return
	}
	clearBackendPing(s.id.XUID)
}
