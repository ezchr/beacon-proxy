package bridge

import (
	"math"
	"slices"
	"strings"

	"github.com/ezchr/beacon-proxy/internal/proxy"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The relay's own commands for players on a Network, handled before their server sees them:
//
//	/server [name]                 list the servers, or move to one
//	/ping [player|@a|@r|...]       your ping or other players' (anyone; see ping.go)
//	/broadcast [on|off|status]     list the world in the Friends tab or take it off (admins only)
//
// They are added to the command list every server sends, so they autocomplete everywhere. A
// /server name that isn't one of the relay's servers goes on to the server (Velocity has its own).

// geyserPlayerSubcommands are the /geyser subcommands every player may use (Geyser's permission
// defaults); geyserAdminSubcommands are added for the relay's admins.
var (
	geyserPlayerSubcommands = []string{"advancements", "statistics", "settings", "options", "tooltips", "quickactions", "offhand", "ping", "help"}
	geyserAdminSubcommands  = []string{"list", "reload", "dump", "version", "connectiontest", "gamerules", "nethernet", "extensions"}
)

// hiddenForPlayers are command names left out of non-admins' command lists: Velocity's LuckPerms
// command and its click-callback command, which players have no use for.
var hiddenForPlayers = map[string]bool{"lpv": true, "luckpermsvelocity": true, "velocity:callback": true}

// injectCommands adds the relay's commands to a server's command list.
func (s *switchSession) injectCommands(raw, payload []byte) []byte {
	var pk packet.AvailableCommands
	if decodeInto(&pk, payload) != nil {
		return raw
	}
	changed := false
	if !s.isAdmin() {
		// Proxy-internal commands Velocity adds for everyone; only admins need to see them.
		kept := pk.Commands[:0]
		for _, c := range pk.Commands {
			if hiddenForPlayers[c.Name] {
				changed = true
				continue
			}
			kept = append(kept, c)
		}
		pk.Commands = kept
	}
	have := map[string]bool{}
	for _, c := range pk.Commands {
		have[c.Name] = true
	}
	// /geyser reaches Bedrock without its subcommands (Geyser registers it on Velocity as a plain
	// command): give it the ones this player may use, so they are suggested.
	for i, c := range pk.Commands {
		if c.Name != "geyser" {
			continue
		}
		subs := geyserPlayerSubcommands
		if s.isAdmin() {
			subs = append(append([]string(nil), subs...), geyserAdminSubcommands...)
		}
		e := protocol.CommandEnum{Type: "BeaconGeyser"}
		for _, v := range subs {
			idx := slices.Index(pk.EnumValues, v)
			if idx < 0 {
				idx = len(pk.EnumValues)
				pk.EnumValues = append(pk.EnumValues, v)
			}
			e.ValueIndices = append(e.ValueIndices, uint32(idx))
		}
		pk.Enums = append(pk.Enums, e)
		pk.Commands[i].Overloads = []protocol.CommandOverload{{Parameters: []protocol.CommandParameter{
			{Name: "subcommand", Type: protocol.CommandArgValid | protocol.CommandArgEnum | uint32(len(pk.Enums)-1), Optional: true},
		}}}
		changed = true
	}
	enum := func(name string, values []string) uint32 {
		e := protocol.CommandEnum{Type: name}
		for _, v := range values {
			i := slices.Index(pk.EnumValues, v)
			if i < 0 {
				i = len(pk.EnumValues)
				pk.EnumValues = append(pk.EnumValues, v)
			}
			e.ValueIndices = append(e.ValueIndices, uint32(i))
		}
		pk.Enums = append(pk.Enums, e)
		return uint32(len(pk.Enums) - 1)
	}
	added := false
	serverOverloads := func() []protocol.CommandOverload {
		servers := enum("BeaconServer", s.n.ServerNames())
		return []protocol.CommandOverload{{Parameters: []protocol.CommandParameter{
			{Name: "server", Type: protocol.CommandArgValid | protocol.CommandArgEnum | servers, Optional: true},
		}}}
	}
	if !have["server"] {
		pk.Commands = append(pk.Commands, protocol.Command{
			Name: "server", Description: "Move to another server of this network", AliasesOffset: math.MaxUint32,
			Overloads: serverOverloads(),
		})
		added = true
	} else {
		// A server behind Velocity already lists Velocity's /server, whose server names Java
		// clients get as they type, which Bedrock can't: the relay answers /server itself, so its
		// list of servers replaces Velocity's options.
		for i, c := range pk.Commands {
			if c.Name == "server" {
				pk.Commands[i].Overloads = serverOverloads()
				added = true
			}
		}
	}
	// /ping for everyone: the relay answers it (ping.go), so its form replaces a server's own.
	pingDone := false
	for i, c := range pk.Commands {
		if c.Name == "ping" {
			pk.Commands[i].Description = "Show your ping or another player's"
			pk.Commands[i].Overloads = s.pingOverloads(enum)
			pingDone = true
		}
	}
	if !pingDone {
		pk.Commands = append(pk.Commands, protocol.Command{
			Name: "ping", Description: "Show your ping or another player's", AliasesOffset: math.MaxUint32,
			Overloads: s.pingOverloads(enum),
		})
	}
	added = true
	if s.isAdmin() && !have["glist"] {
		pk.Commands = append(pk.Commands, protocol.Command{
			Name: "glist", Description: "List the players on every server", AliasesOffset: math.MaxUint32,
			Overloads: []protocol.CommandOverload{{}},
		})
		added = true
	}
	if s.isAdmin() && !have["send"] {
		var names []string
		for _, p := range s.n.Players() {
			names = append(names, p.Name)
		}
		who := enum("BeaconPlayer", append(names, "all", "current"))
		servers := enum("BeaconSendServer", s.n.ServerNames())
		pk.Commands = append(pk.Commands, protocol.Command{
			Name: "send", Description: "Move players to another server", AliasesOffset: math.MaxUint32,
			Overloads: []protocol.CommandOverload{{Parameters: []protocol.CommandParameter{
				{Name: "player", Type: protocol.CommandArgValid | protocol.CommandArgEnum | who},
				{Name: "server", Type: protocol.CommandArgValid | protocol.CommandArgEnum | servers},
			}}},
		})
		added = true
	}
	if s.isAdmin() && !have["beacon"] && ReloadHook != nil {
		actions := enum("BeaconAction", []string{"reload"})
		pk.Commands = append(pk.Commands, protocol.Command{
			Name: "beacon", Description: "Beacon Proxy: reload beacon.toml", AliasesOffset: math.MaxUint32,
			Overloads: []protocol.CommandOverload{{Parameters: []protocol.CommandParameter{
				{Name: "action", Type: protocol.CommandArgValid | protocol.CommandArgEnum | actions},
			}}},
		})
		added = true
	}
	if !have["broadcast"] && s.n.Listing != nil && s.isAdmin() {
		states := enum("BeaconBroadcast", []string{"on", "off", "status"})
		worlds := enum("BeaconBroadcastWorld", s.n.Listing.Worlds())
		pk.Commands = append(pk.Commands, protocol.Command{
			Name: "broadcast", Description: "Show or hide worlds in the Friends tab", AliasesOffset: math.MaxUint32,
			Overloads: []protocol.CommandOverload{{Parameters: []protocol.CommandParameter{
				{Name: "state", Type: protocol.CommandArgValid | protocol.CommandArgEnum | states, Optional: true},
				{Name: "world", Type: protocol.CommandArgValid | protocol.CommandArgEnum | worlds, Optional: true},
			}}},
		})
		added = true
	}
	if s.isAdmin() || s.moderator() {
		s.injectModeration(&pk, enum)
		added = true
	}
	if !added && !changed {
		return raw
	}
	return encodePacket(&pk)
}

// ReloadHook reloads beacon.toml and describes what changed (/beacon reload); set by the program.
var ReloadHook func() []string

func (s *switchSession) isAdmin() bool {
	return s.n.IsAdmin(s.id.XUID, s.id.DisplayName)
}

// handleCommand runs a relay command and reports whether it was one (so it must not reach the server).
func (s *switchSession) handleCommand(line string) bool {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "server":
		if len(fields) == 1 {
			cur := s.serverName()
			var names []string
			for _, sc := range s.n.conf().servers {
				n := sc.Name
				if strings.EqualFold(n, cur) {
					n = "<green>" + n + "</green>"
				} else if !sc.Accepts(s.proto) {
					n += " <grey>(not on " + proxy.VersionName(s.proto) + " yet)</grey>"
				}
				names = append(names, n)
			}
			s.message("<yellow>You are on %s. Servers: %s. Use /server <name>.</yellow>", cur, strings.Join(names, ", "))
			return true
		}
		server := s.n.server(fields[1])
		if server == nil {
			return false // maybe the server's own /server (Velocity)
		}
		if err := s.requestSwitch(*server, ""); err != nil {
			s.message("<red>%s</red>", err)
		}
		return true
	case "ping":
		return s.pingCommand(fields[1:])
	case "glist":
		if !s.isAdmin() {
			return false // the server's own /glist, if it has one
		}
		for _, line := range s.n.GList() {
			s.message("<yellow>%s</yellow>", line)
		}
		return true
	case "send":
		if !s.isAdmin() {
			return false
		}
		if len(fields) != 3 {
			s.message("<red>Usage: /send <player|all|current> <server></red>")
			return true
		}
		who := fields[1]
		if strings.EqualFold(who, "current") {
			who = "current:" + s.serverName()
		}
		moved, err := s.n.Send(who, fields[2])
		if err != nil {
			s.message("<red>%s</red>", err)
			return true
		}
		s.message("<green>Moving %d player(s) to %s.</green>", moved, fields[2])
		return true
	case "beacon":
		if !s.isAdmin() || ReloadHook == nil {
			return false
		}
		if len(fields) != 2 || !strings.EqualFold(fields[1], "reload") {
			s.message("<red>Usage: /beacon reload</red>")
			return true
		}
		for _, line := range ReloadHook() {
			s.message("<yellow>%s</yellow>", line)
		}
		return true
	case "broadcast":
		if s.n.Listing == nil {
			return false
		}
		if !s.isAdmin() {
			s.message("<red>You don't have permission to use this command.</red>")
			return true
		}
		arg := "status"
		if len(fields) > 1 {
			arg = strings.ToLower(fields[1])
		}
		world := ""
		if len(fields) > 2 {
			world = fields[2]
		}
		switch arg {
		case "on", "off":
			var err error
			if world != "" {
				err = s.n.Listing.SetWorld(world, arg == "on")
			} else {
				err = s.n.Listing.Set(arg == "on")
			}
			if err != nil {
				s.message("<red>Could not switch the broadcast %s: %s</red>", arg, err)
				return true
			}
			s.n.Log.Info("network: broadcast switched by a player", "player", s.id.DisplayName, "on", arg == "on", "world", world)
			fallthrough
		case "status":
			for _, w := range s.n.Listing.Worlds() {
				if s.n.Listing.WorldOn(w) {
					s.message("<green>%s: listed in the Friends tab.</green>", w)
				} else {
					s.message("<yellow>%s: not listed (players already in stay, direct IP still works).</yellow>", w)
				}
			}
		default:
			s.message("<red>Usage: /broadcast [on|off|status] [world]</red>")
		}
		return true
	}
	return false
}
