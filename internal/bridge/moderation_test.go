package bridge

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSplitCommand(t *testing.T) {
	got := splitCommand(`/ban "Some Player" 7d being rude`)
	want := []string{"ban", "Some Player", "7d", "being", "rude"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestParseModTime(t *testing.T) {
	for s, want := range map[string]time.Duration{"30m": 30 * time.Minute, "12h": 12 * time.Hour, "7d": 168 * time.Hour, "2w": 336 * time.Hour, "perm": 0} {
		if got, ok := parseModTime(s); !ok || got != want {
			t.Fatalf("%s: %v %v", s, got, ok)
		}
	}
	for _, s := range []string{"rude", "0d", "7", "d"} {
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
