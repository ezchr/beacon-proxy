package bridge

import (
	"strings"
	"sync"
	"sync/atomic"
)

// /kick <player> [reason], for admins with the other moderation commands. kick in beacon.toml says
// what it does:
//
//	"network" (default)  disconnects the player from the network; they may join again at once
//	"server"             takes them off their server only: they go to another of your servers, as
//	                     when a server kicks them (disconnected only when there is no other)
//
// It works on Bedrock players, and on Java players a Java proxy reports (moderation_java.go): the
// proxy does those with its next report, a moment later.

var kickToServer atomic.Bool

// SetKickMode sets what /kick does: "server" takes players off their server only; anything else
// disconnects them from the network.
func SetKickMode(mode string) {
	kickToServer.Store(strings.EqualFold(strings.TrimSpace(mode), "server"))
}

// kickMessage is what a kicked player reads.
func kickMessage(server, reason string) string {
	msg := "You were kicked"
	if server != "" {
		msg += " from " + server
	}
	if reason != "" {
		msg += ": " + reason
	}
	return msg + "."
}

// javaKicks are kicks waiting for the Java proxy's next report, by lower-case UUID.
var (
	javaKicksMu sync.Mutex
	javaKicks   = map[string]JavaKick{}
)

// JavaKick is a kick the Java proxy carries out: Message for the player, and ToServer when they go
// to another server instead of leaving the network.
type JavaKick struct {
	Message  string `json:"message"`
	ToServer bool   `json:"to_server,omitempty"`
}

// takeJavaKick returns, once, the kick waiting for a Java player.
func takeJavaKick(uuid string) (JavaKick, bool) {
	javaKicksMu.Lock()
	defer javaKicksMu.Unlock()
	k, ok := javaKicks[strings.ToLower(uuid)]
	delete(javaKicks, strings.ToLower(uuid))
	return k, ok
}

// kick runs /kick. It reports false for a name that is not on, which goes on to the server.
func (s *switchSession) kick(who string, args []string) bool {
	if isProtected(who) {
		s.message(protectedMessage, who)
		return true
	}
	reason := strings.TrimSpace(strings.Join(args, " "))
	toServer := kickToServer.Load()
	if p, ok := s.n.FindPlayer(who); ok {
		v, on := s.n.sessions.Load(p.XUID)
		if !on {
			s.message("<red>%s is not online.</red>", p.Name)
			return true
		}
		o := v.(*switchSession)
		if s.reply == nil && s.n.IsAdmin(o.id.XUID, o.id.DisplayName) { // the console may
			s.message("<red>%s is an admin.</red>", p.Name)
			return true
		}
		if toServer {
			from := o.serverName()
			for _, other := range s.n.orderedServers() {
				if strings.EqualFold(other.Name, from) || !other.Accepts(o.proto) {
					continue
				}
				if err := o.requestSwitch(other, kickMessage(from, reason)); err == nil {
					s.message("<green>Kicked %s from %s.</green>", p.Name, from)
					return true
				}
			}
		}
		o.disconnect(kickMessage("", reason))
		s.message("<green>Kicked %s.</green>", p.Name)
		return true
	}
	for _, j := range javaPlayersOn() {
		if !strings.EqualFold(j.name, who) || j.uuid == "" {
			continue
		}
		msg := kickMessage("", reason)
		if toServer {
			msg = kickMessage(j.server, reason)
		}
		javaKicksMu.Lock()
		javaKicks[strings.ToLower(j.uuid)] = JavaKick{Message: msg, ToServer: toServer}
		javaKicksMu.Unlock()
		s.message("<green>Kicked %s.</green>", j.name)
		return true
	}
	if s.reply != nil {
		s.message("<red>%s is not online.</red>", who)
		return true
	}
	return false
}
