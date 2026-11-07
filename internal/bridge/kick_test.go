package bridge

import (
	"strings"
	"testing"
)

// /kick on a Java player reported by the Java proxy waits for the proxy's next report, once, and
// says whether the player leaves the network or only their server.
func TestKickJavaPlayer(t *testing.T) {
	wasOn := !moderationOff.Load()
	SetModeration(true)
	defer SetModeration(wasOn)
	defer SetKickMode("network")
	n := &Network{Dir: t.TempDir(), Servers: []ServerConfig{{Name: "pvp"}, {Name: "skygen"}}}
	const steve = "11111111-2222-3333-4444-555555555555"
	report := func() map[string]JavaStatus {
		return n.JavaState([]JavaPlayer{{UUID: steve, Name: "Steve", Ping: 30, Server: "pvp"}})
	}
	defer noteJavaPings(nil)
	report()

	if out := strings.Join(n.ModerateAs("kick Steve spamming", "Mod", false), "\n"); !strings.Contains(out, "Kicked Steve") {
		t.Fatalf("kick: %q", out)
	}
	st := report()[steve]
	if st.Kick == nil || st.Kick.ToServer || st.Kick.Message != "You were kicked: spamming." {
		t.Fatalf("network kick: %+v", st.Kick)
	}
	if report()[steve].Kick != nil {
		t.Fatal("a kick was given twice")
	}

	SetKickMode("server")
	n.ModerateAs("kick Steve", "Mod", false)
	st = report()[steve]
	if st.Kick == nil || !st.Kick.ToServer || st.Kick.Message != "You were kicked from pvp." {
		t.Fatalf("server kick: %+v", st.Kick)
	}

	if out := strings.Join(n.ModerateAs("kick Nobody", "Mod", false), "\n"); !strings.Contains(out, "not online") {
		t.Fatalf("kick of nobody: %q", out)
	}
	if out := strings.Join(n.ModerateAs("kick", "Mod", false), "\n"); !strings.Contains(out, "/kick <player>") {
		t.Fatalf("kick without a name: %q", out)
	}
}
