package bridge

import (
	"strings"
	"testing"
)

// Moderators ban for at most a day, unban only their own bans, and have no address bans.
func TestModeratorLimits(t *testing.T) {
	wasOn := !moderationOff.Load()
	SetModeration(true)
	defer SetModeration(wasOn)
	n := &Network{Dir: t.TempDir()}
	n.moderation().see("100", "Steve", "203.0.113.5")
	n.moderation().see("200", "Alex", "203.0.113.6")
	say := func(line, by string, mod bool) string { return strings.Join(n.ModerateAs(line, by, mod), "\n") }

	for _, line := range []string{"ban Steve", "ban Steve perm", "ban Steve 2d", "ban Steve 25h griefing"} {
		if out := say(line, "Mod", true); !strings.Contains(out, "up to 1 day") {
			t.Errorf("%q by a moderator: %q", line, out)
		}
	}
	if out := say("ban Steve 1d griefing", "Mod", true); !strings.Contains(out, "Banned Steve") {
		t.Fatalf("a 1-day ban by a moderator: %q", out)
	}
	if out := say("ban Alex 12h", "Admin", false); !strings.Contains(out, "Banned Alex") {
		t.Fatalf("an admin's ban: %q", out)
	}
	if out := say("unban Alex", "Mod", true); !strings.Contains(out, "only unban players you banned") {
		t.Fatalf("a moderator lifted someone else's ban: %q", out)
	}
	if out := say("unban Steve", "Other", true); !strings.Contains(out, "only unban players you banned") {
		t.Fatalf("another moderator lifted it: %q", out)
	}
	if out := say("unban Steve", "Mod", true); !strings.Contains(out, "no longer banned") {
		t.Fatalf("a moderator could not lift their own ban: %q", out)
	}
	if out := say("banip Steve", "Mod", true); !strings.Contains(out, "can't use /banip") {
		t.Fatalf("banip by a moderator: %q", out)
	}
	if out := say("mute Steve 3d spam", "Mod", true); !strings.Contains(out, "up to 1 day") {
		t.Fatalf("a 3-day mute by a moderator: %q", out)
	}
	if out := say("mute Steve 1h spam", "Mod", true); !strings.Contains(out, "Muted Steve") {
		t.Fatalf("a 1-hour mute by a moderator: %q", out)
	}

	SetModerators([]string{"200"})
	defer SetModerators(nil)
	if out := say("kick Alex", "Mod", true); !strings.Contains(out, "is staff") {
		t.Fatalf("a moderator acted on a moderator: %q", out)
	}
	if !isModerator("200", "") || isModerator("", "Alex") {
		t.Fatal("moderators are by XUID only")
	}
}
