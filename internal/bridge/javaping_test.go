package bridge

import (
	"strings"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// Java players the Java proxy reports are in /ping (by name, @a, @r) and /glist with Bedrock ones.
func TestJavaPlayersInPingAndGList(t *testing.T) {
	n := &Network{Dir: t.TempDir(), Servers: []ServerConfig{{Name: "pvp"}, {Name: "skygen"}}}
	me := &switchSession{n: n, id: login.IdentityData{XUID: "1", DisplayName: "Me"}}
	n.sessions.Store("1", me)
	e := registerPing("1", fixedLatency(20*time.Millisecond))
	defer unregisterPing("1", e)
	n.JavaState([]JavaPlayer{{UUID: "11111111-2222-3333-4444-555555555555", Name: "Steve", Ping: 33, Server: "skygen"}})
	defer noteJavaPings(nil)

	if lines, ok := n.RunPing("steve"); !ok || strings.Join(lines, "\n") != "Steve's ping: 33 ms" {
		t.Fatalf("a Java player by name: %v %q", ok, lines)
	}
	if lines, ok := n.RunPing("@a"); !ok || !strings.Contains(strings.Join(lines, "\n"), "Me's ping: 40 ms") || !strings.Contains(strings.Join(lines, "\n"), "Steve's ping: 33 ms") {
		t.Fatalf("@a: %v %q", ok, lines)
	}
	if _, ok := n.RunPing("Nobody"); ok {
		t.Fatal("a name nobody has was answered")
	}
	g := strings.Join(n.GList(), "\n")
	if !strings.Contains(g, "2 players online (1 Bedrock, 1 Java).") || !strings.Contains(g, "[skygen] (1): Steve") {
		t.Fatalf("glist: %q", g)
	}
	if names := n.onlineNames(); len(names) != 2 || names[0] != "Me" || names[1] != "Steve" {
		t.Fatalf("suggested names: %v", names)
	}
	noteJavaPings(nil)
	if _, ok := n.RunPing("Steve"); ok {
		t.Fatal("a Java player who left is still pinged")
	}
}
