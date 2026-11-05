package bridge

import (
	"slices"
	"testing"
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
	if m.over(2) || !m.over(2) {
		t.Fatal("packet limit not enforced")
	}
	SetLimits(Limits{})
	if loginTooSoon("203.0.113.9") {
		t.Fatal("login limit enforced while off")
	}
}
