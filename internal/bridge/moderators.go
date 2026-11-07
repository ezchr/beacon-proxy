package bridge

import (
	"strings"
	"sync/atomic"
	"time"
)

// Moderators (moderators in beacon.toml, by XUID) have part of the admins' moderation
// commands: /kick, /mute, /unmute, /mutelist, /banlist, and /ban for at most moderatorMaxBan, with
// /unban only on the bans they made themselves. They can't act on admins or other moderators. A
// Java proxy can give the same to Java players (moderation_java.go, role=moderator).

// moderatorMaxBan is the longest ban a moderator may give.
const moderatorMaxBan = 24 * time.Hour

// moderatorCommands are the moderation commands moderators have.
var moderatorCommands = map[string]bool{
	"kick": true, "mute": true, "unmute": true, "mutelist": true, "banlist": true, "ban": true, "unban": true,
}

var moderators atomic.Pointer[map[string]bool]

// SetModerators sets the moderators, by XUID.
func SetModerators(list []string) {
	set := map[string]bool{}
	for _, m := range list {
		if m = strings.TrimSpace(m); m != "" {
			set[m] = true
		}
	}
	moderators.Store(&set)
}

// isModerator reports whether a player is one of the moderators. By XUID only: a gamertag can be
// renamed, and taken by someone else. name is not used.
func isModerator(xuid, _ string) bool {
	set := moderators.Load()
	return set != nil && xuid != "" && (*set)[xuid]
}

// moderator reports whether this session acts as a moderator: a player in game who is one (and not
// an admin), or a Java proxy's moderator (ModerateAs).
func (s *switchSession) moderator() bool {
	if s.reply != nil {
		return s.limited
	}
	return !s.isAdmin() && isModerator(s.id.XUID, s.id.DisplayName)
}

// byID is who made a ban, to know later whether they may lift it: an XUID, or java:<name>.
func (s *switchSession) byID() string { return s.id.XUID }

// moderatorRefuses checks a moderator's command and says why it may not run, if so.
func (s *switchSession) moderatorRefuses(name string, args []string) (string, bool) {
	if !moderatorCommands[name] {
		return "Moderators can't use /" + name + ".", true
	}
	if len(args) == 0 {
		return "", false
	}
	switch name {
	case "ban", "mute", "kick":
		if t, ok := s.n.findTarget(args[0]); ok && !isJavaKey(t.xuid) &&
			(s.n.IsAdmin(t.xuid, t.name) || isModerator(t.xuid, t.name)) {
			return t.name + " is staff.", true
		}
	}
	if name == "ban" {
		d, _ := modArgs(args[1:])
		if d <= 0 || d > moderatorMaxBan {
			return "Moderators can ban for up to 1 day: /ban <player> <time, up to 1d> [reason].", true
		}
	}
	if name == "unban" {
		m := s.n.moderation()
		key, ok := s.n.listedTarget(m.Bans, args[0])
		if ok {
			m.mu.Lock()
			e := m.Bans[key]
			m.mu.Unlock()
			if e.ByID == "" || e.ByID != s.byID() {
				return "You can only unban players you banned.", true
			}
		}
	}
	return "", false
}
