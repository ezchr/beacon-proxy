package bridge

import (
	"strings"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

type fixedLatency time.Duration

func (f fixedLatency) Latency() time.Duration { return time.Duration(f) }

// /ping answers the player who typed it: their own ping, a named player's, or a selector's.
func TestPingCommand(t *testing.T) {
	n := &Network{}
	var got []string
	me := &switchSession{n: n, id: login.IdentityData{XUID: "1", DisplayName: "Me"}, reply: func(m string) { got = append(got, m) }}
	other := &switchSession{n: n, id: login.IdentityData{XUID: "2", DisplayName: "The Lloyd Xmas"}}
	n.sessions.Store("1", me)
	n.sessions.Store("2", other)
	e1 := registerPing("1", fixedLatency(20*time.Millisecond))
	defer unregisterPing("1", e1)
	e2 := registerPing("2", fixedLatency(60*time.Millisecond))
	defer unregisterPing("2", e2)

	run := func(line string) (bool, string) {
		got = nil
		handled := me.handleCommand(line)
		return handled, strings.Join(got, "\n")
	}
	for _, line := range []string{"/ping", "/ping @s", "/ping @p", "/PING"} {
		if ok, out := run(line); !ok || !strings.Contains(out, "Your ping: 40 ms") {
			t.Errorf("%s: handled=%v %q", line, ok, out)
		}
	}
	if ok, out := run(`/ping "The Lloyd Xmas"`); !ok || !strings.Contains(out, "The Lloyd Xmas's ping: 120 ms") {
		t.Errorf("named player: handled=%v %q", ok, out)
	}
	if ok, out := run("/ping the lloyd xmas"); !ok || !strings.Contains(out, "The Lloyd Xmas") {
		t.Errorf("name without quotes, any case: handled=%v %q", ok, out)
	}
	if ok, out := run("/ping @a"); !ok || !strings.Contains(out, "Me") || !strings.Contains(out, "The Lloyd Xmas") {
		t.Errorf("@a: handled=%v %q", ok, out)
	}
	if ok, out := run("/ping @a[r=5]"); !ok || !strings.Contains(out, "The Lloyd Xmas") {
		t.Errorf("@a with arguments: handled=%v %q", ok, out)
	}
	if ok, out := run("/ping @r"); !ok || !strings.Contains(out, " ms") {
		t.Errorf("@r: handled=%v %q", ok, out)
	}
	if ok, out := run("/ping @x"); !ok || !strings.Contains(out, "Use @s") {
		t.Errorf("unknown selector: handled=%v %q", ok, out)
	}
	if ok, _ := run("/ping SomeJavaPlayer"); ok {
		t.Error("a name the relay does not know was not left to the server")
	}
}
