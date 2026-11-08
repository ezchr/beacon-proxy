package bridge

import (
	"slices"
	"testing"
	"time"
)

func names(servers []ServerConfig) []string {
	var out []string
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}

func TestJoinOrder(t *testing.T) {
	n := &Network{}
	servers := []ServerConfig{{Name: "lobby"}, {Name: "survival"}, {Name: "pvp"}}
	forced := map[string][]string{"PvP.Example.com": {"pvp", "lobby"}}
	if err := n.setLive(servers, []string{"lobby", "survival"}, forced, nil); err != nil {
		t.Fatal(err)
	}
	for typed, want := range map[string][]string{
		"":                               {"lobby", "survival"},
		"https://play.example.com:19132": {"lobby", "survival"},
		"https://pvp.example.com:19132":  {"pvp", "lobby"},
		"pvp.example.com":                {"pvp", "lobby"},
	} {
		if got := names(n.joinOrder(2225, typed)); !slices.Equal(got, want) {
			t.Errorf("%q: got %v, want %v", typed, got, want)
		}
	}
	if n.DefaultServer() != "lobby" {
		t.Fatal("default", n.DefaultServer())
	}
	if err := n.setLive(servers, []string{"nope"}, nil, nil); err == nil {
		t.Fatal("try with an unknown server accepted")
	}
	if err := n.setLive(servers, []string{"lobby"}, map[string][]string{"x.example.com": {"nope"}}, nil); err == nil {
		t.Fatal("forced host with an unknown server accepted")
	}
}

func TestLimits(t *testing.T) {
	SetLimits(Limits{LoginInterval: 50_000_000, PacketsPerSecond: 3})
	defer SetLimits(Limits{})
	if loginTooSoon("203.0.113.9") || !loginTooSoon("203.0.113.9") {
		t.Fatal("login interval not enforced")
	}
	var m packetMeter
	if m.over(5) { // a burst over the limit, within the average and under packetBurst times it
		t.Fatal("a short burst was refused")
	}
	if !m.over(3 * defaultPacketBurst) {
		t.Fatal("a flood in one second passed")
	}
	avg := packetMeter{second: time.Now().Unix() + 1} // the seconds before were all busy, at the limit
	for i := range defaultPacketWindow {
		avg.buckets[i] = 3
	}
	avg.buckets[avg.cur] = 0
	if avg.over(3) || !avg.over(1) {
		t.Fatal("the average over the window not enforced")
	}
	SetLimits(Limits{})
	if loginTooSoon("203.0.113.9") {
		t.Fatal("login limit enforced while off")
	}
}

// A world's server that try leaves out still comes first: try = ["pvp"], world server "skygen".
func TestWithFirstServer(t *testing.T) {
	n := &Network{}
	servers := []ServerConfig{{Name: "pvp"}, {Name: "skygen"}, {Name: "old", Protocols: []int32{1}}}
	if err := n.setLive(servers, []string{"pvp"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	for first, want := range map[string][]string{
		"":       {"pvp"},
		"skygen": {"skygen", "pvp"},
		"SkyGen": {"skygen", "pvp"},
		"pvp":    {"pvp"},
		"nope":   {"pvp"},
		"old":    {"pvp"}, // does not take this client's version
	} {
		if got := names(n.withFirstServer(n.joinOrder(2225, ""), first, 2225)); !slices.Equal(got, want) {
			t.Errorf("first %q: got %v, want %v", first, got, want)
		}
	}
}
