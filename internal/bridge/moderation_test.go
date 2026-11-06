package bridge

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func TestSplitCommand(t *testing.T) {
	got := splitCommand(`/ban "Some Player" 7d being rude`)
	want := []string{"ban", "Some Player", "7d", "being", "rude"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestParseModTime(t *testing.T) {
	for s, want := range map[string]time.Duration{"30m": 30 * time.Minute, "12h": 12 * time.Hour, "7d": 168 * time.Hour, "2w": 336 * time.Hour, "perm": 0, "7days": 168 * time.Hour, "1hour": time.Hour, "30mins": 30 * time.Minute, "1month": 720 * time.Hour} {
		if got, ok := parseModTime(s); !ok || got != want {
			t.Fatalf("%s: %v %v", s, got, ok)
		}
	}
	for _, s := range []string{"rude", "0d", "7", "d", "7x", "1v1"} {
		if _, ok := parseModTime(s); ok {
			t.Fatalf("%s read as a time", s)
		}
	}
}

func TestModArgs(t *testing.T) {
	if d, r := modArgs([]string{"7d", "being", "rude"}); d != 168*time.Hour || r != "being rude" {
		t.Fatalf("%v %q", d, r)
	}
	if d, r := modArgs([]string{"being", "rude"}); d != 0 || r != "being rude" {
		t.Fatalf("%v %q", d, r)
	}
}

func TestBadModTime(t *testing.T) {
	for _, args := range [][]string{{"7x"}, {"7"}, {"0d", "rude"}, {"12dayz"}} {
		if !badModTime(args) {
			t.Fatalf("%v passed", args)
		}
	}
	for _, args := range [][]string{nil, {"7d"}, {"perm"}, {"being", "rude"}, {"1v1", "cheating"}} {
		if badModTime(args) {
			t.Fatalf("%v refused", args)
		}
	}
}

func TestModLeft(t *testing.T) {
	now := time.Unix(1000000, 0)
	for d, want := range map[time.Duration]string{
		30 * time.Second: "1 minute",
		45 * time.Minute: "45 minutes",
		time.Hour:        "60 minutes",
		2 * time.Hour:    "2 hours",
		47 * time.Hour:   "47 hours",
		72 * time.Hour:   "3 days",
		168 * time.Hour:  "7 days",
	} {
		if got := (modEntry{Until: now.Add(d).Unix()}).left(now); got != want {
			t.Fatalf("%v: %q", d, got)
		}
	}
	if got := (modEntry{}).left(now); got != "permanent" {
		t.Fatal(got)
	}
}

func TestModerateConsole(t *testing.T) {
	n := &Network{Dir: t.TempDir()}
	n.moderation().see("1", "Steve", "203.0.113.1")
	for _, c := range [][2]string{
		{"ban", "Use /ban <player> [time] [reason]"},
		{"nonsense", "ban <player> [time] [reason]"},
		{"ban Nobody", "Beacon doesn't know Nobody. For"},
		{"unmute Steve", "Steve is not muted."},
		{"unban Steve", "Steve is not banned."},
		{"ban Steve 7x", "7x is not a time."},
		{"ban Steve 7days", "Banned Steve (7 days)."},
		{"mute Steve 2h", "Muted Steve (2 hours)."},
	} {
		if out := n.Moderate(c[0]); len(out) == 0 || !strings.HasPrefix(out[0], c[1]) {
			t.Fatalf("%s: %q", c[0], out)
		}
	}
}

func TestInjectModeration(t *testing.T) {
	n := &Network{Dir: t.TempDir()}
	n.moderation().see("1", "Steve", "203.0.113.1")
	n.Moderate("ban Steve")
	s := &switchSession{n: n}
	enums := map[string][]string{}
	pk := &packet.AvailableCommands{}
	s.injectModeration(pk, func(name string, values []string) uint32 {
		enums[name] = values
		return 0
	})
	names := map[string]bool{}
	for _, c := range pk.Commands {
		names[c.Name] = true
	}
	for _, want := range []string{"ban", "banip", "mute", "unban", "unbanip", "unmute", "banlist", "mutelist"} {
		if !names[want] {
			t.Fatalf("no /%s in %v", want, names)
		}
	}
	if !slices.Equal(enums["BeaconBanned"], []string{"Steve"}) {
		t.Fatalf("/unban suggests %v", enums["BeaconBanned"])
	}
}

func TestModStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moderation.json")
	m := newModStore(path)
	now := time.Now()
	if err := m.put(m.Bans, "1", modEntry{XUID: "1", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	_ = m.put(m.Bans, "2", modEntry{XUID: "2", Until: now.Add(-time.Minute).Unix()})
	_ = m.put(m.IPBans, "203.0.113.1", modEntry{IP: "203.0.113.1"})
	_ = m.put(m.Mutes, "3", modEntry{XUID: "3", Until: now.Add(time.Hour).Unix()})
	m.see("9", "Nine", "203.0.113.9")

	m = newModStore(path) // a restart
	if _, ok := m.refused("1", "", now); !ok {
		t.Fatal("ban lost")
	}
	if _, ok := m.refused("2", "", now); ok {
		t.Fatal("over ban held")
	}
	if _, ok := m.refused("5", "203.0.113.1", now); !ok {
		t.Fatal("address ban lost")
	}
	if _, ok := m.muted("3", now); !ok {
		t.Fatal("mute lost")
	}
	if _, ok := m.muted("3", now.Add(2*time.Hour)); ok {
		t.Fatal("over mute held")
	}
	if p := m.Seen["nine"]; p.XUID != "9" {
		t.Fatalf("seen lost: %v", p)
	}
	if was, _ := m.remove(m.Bans, "1"); !was {
		t.Fatal("not removed")
	}
	if _, ok := m.refused("1", "", now); ok {
		t.Fatal("still banned")
	}
}
