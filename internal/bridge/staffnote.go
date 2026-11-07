package bridge

import (
	"strings"
)

// staffCommands are Beacon's own commands for admins and moderators. Running one is logged and
// shown to the other staff online, as Dragonfly's admin commands are: [Name: /ban Steve 1d].
var staffCommands = map[string]bool{
	"ban": true, "unban": true, "banip": true, "unbanip": true, "mute": true, "unmute": true, "kick": true,
	"banlist": true, "mutelist": true, "glist": true, "send": true, "broadcast": true, "beacon": true, "admin": true,
}

// noteStaffCommand logs a staff command this session ran and shows it to the other staff online.
func (s *switchSession) noteStaffCommand(line string) {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	if len(fields) == 0 || !staffCommands[strings.ToLower(fields[0])] || (!s.isAdmin() && !s.moderator()) {
		return
	}
	cmd := "/" + strings.Join(fields, " ")
	s.log.Info("staff command", "command", cmd)
	s.n.sessions.Range(func(_, v any) bool {
		o := v.(*switchSession)
		if o != s && (o.isAdmin() || o.moderator()) {
			o.message("<grey><italic>[%s: %s]</italic></grey>", s.id.DisplayName, escapeTags(cmd))
		}
		return true
	})
}

// escapeTags keeps a typed command from being read as formatting: < becomes a lookalike.
func escapeTags(s string) string { return strings.ReplaceAll(s, "<", "‹") }
