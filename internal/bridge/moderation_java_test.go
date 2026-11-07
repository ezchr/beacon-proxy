package bridge

import (
	"strings"
	"testing"
)

// Java players reported by the Java proxy share Beacon's bans and mutes: banned or muted by name,
// refused at login, and removed or silenced while on.
func TestJavaModeration(t *testing.T) {
	wasOn := !moderationOff.Load()
	SetModeration(true)
	defer SetModeration(wasOn)
	n := &Network{Dir: t.TempDir(), Admins: map[string]bool{"2535000000000001": true}}
	const steve, impostor = "11111111-2222-3333-4444-555555555555", "99999999-2222-3333-4444-555555555555"

	if _, banned := n.JavaLogin(steve, "Steve", "203.0.113.5"); banned {
		t.Fatal("a new Java player was refused")
	}
	n.JavaLogin(impostor, "AdminTag", "203.0.113.9") // a cracked player using an admin's name

	out := strings.Join(n.ModerateAs("mute Steve 1h spam", "Mod", false), "\n")
	if !strings.Contains(out, "Muted Steve") {
		t.Fatalf("mute: %q", out)
	}
	st := n.JavaState([]JavaPlayer{{UUID: steve, Name: "Steve", IP: "203.0.113.5"}})
	if st[steve].Mute == "" || st[steve].Ban != "" {
		t.Fatalf("muted Steve: %+v", st)
	}
	out = strings.Join(n.ModerateAs("ban java:Steve 7d griefing", "Mod", false), "\n")
	if !strings.Contains(out, "Banned Steve") {
		t.Fatalf("ban: %q", out)
	}
	if msg, banned := n.JavaLogin(steve, "Steve", "203.0.113.5"); !banned || !strings.Contains(msg, "griefing") {
		t.Fatalf("banned Steve logged in: %v %q", banned, msg)
	}
	if st := n.JavaState([]JavaPlayer{{UUID: steve, Name: "Steve"}}); st[steve].Ban == "" {
		t.Fatal("banned Steve is not removed while on")
	}
	n.ModerateAs("unban Steve", "Mod", false)
	if _, banned := n.JavaLogin(steve, "Steve", "203.0.113.5"); banned {
		t.Fatal("unbanned Steve was refused")
	}

	// An address ban stops a Java player on that address, even one with an admin's name.
	out = strings.Join(n.ModerateAs("banip java:AdminTag", "Mod", false), "\n")
	if !strings.Contains(out, "Banned the address") {
		t.Fatalf("banip of the impostor: %q", out)
	}
	if _, banned := n.JavaLogin(impostor, "AdminTag", "203.0.113.9"); !banned {
		t.Fatal("the impostor's address ban did not stop them")
	}

	// Java admins may not ban Beacon's admins, as the console may.
	n.moderation().see("2535000000000001", "AdminTag", "198.51.100.1")
	if out := strings.Join(n.ModerateAs("ban AdminTag", "Mod", false), "\n"); !strings.Contains(out, "is an admin") {
		t.Fatalf("a Java admin banned a Beacon admin: %q", out)
	}
}
