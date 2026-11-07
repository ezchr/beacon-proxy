package bridge

import (
	"log/slog"
	"strings"
	"sync"
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
	s.n.NoteStaff(s.id.DisplayName, "/"+strings.Join(fields, " "), false)
}

// StaffNote is a staff command someone ran, for the staff of both editions to see.
type StaffNote struct {
	Seq     int64  `json:"seq"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Java    bool   `json:"java"` // run by a Java player, on the Java proxy
}

var (
	staffNotesMu sync.Mutex
	staffNotes   []StaffNote // the last staffNotesKept
	staffNoteSeq int64
)

const staffNotesKept = 100

// NoteStaff logs a staff command, shows it to the Bedrock staff online (not to who ran it) and keeps
// it for the Java proxy, which shows it to its staff (StaffNotesAfter).
func (n *Network) NoteStaff(name, command string, java bool) {
	slog.Info("staff command", "player", name, "command", command, "java", java)
	staffNotesMu.Lock()
	staffNoteSeq++
	staffNotes = append(staffNotes, StaffNote{Seq: staffNoteSeq, Name: name, Command: command, Java: java})
	if len(staffNotes) > staffNotesKept {
		staffNotes = staffNotes[len(staffNotes)-staffNotesKept:]
	}
	staffNotesMu.Unlock()
	n.sessions.Range(func(_, v any) bool {
		o := v.(*switchSession)
		if (java || !strings.EqualFold(o.id.DisplayName, name)) && (o.isAdmin() || o.moderator()) {
			o.message("<grey><italic>[%s: %s]</italic></grey>", escapeTags(name), escapeTags(command))
		}
		return true
	})
}

// StaffNotesAfter returns the staff commands after seq and the newest seq.
func StaffNotesAfter(seq int64) ([]StaffNote, int64) {
	staffNotesMu.Lock()
	defer staffNotesMu.Unlock()
	var out []StaffNote
	for _, n := range staffNotes {
		if n.Seq > seq {
			out = append(out, n)
		}
	}
	return out, staffNoteSeq
}

// escapeTags keeps a typed command from being read as formatting: < becomes a lookalike.
func escapeTags(s string) string { return strings.ReplaceAll(s, "<", "‹") }
