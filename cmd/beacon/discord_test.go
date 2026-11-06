package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiscordText(t *testing.T) {
	got := discordText("§c<gradient:1:m>Owner</gradient> §fbob_1: *hi* @everyone")
	if want := `Owner bob\_1: \*hi\* @everyone`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDiscordName(t *testing.T) {
	if got := discordName("§cMyDiscordGuy"); got != "My-------Guy" {
		t.Fatalf("got %q", got)
	}
}

func discordFixture(t *testing.T) (*discordBridge, chan map[string]any) {
	got := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got <- m
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	d := &discordBridge{lines: make(chan discordEntry, 8), client: srv.Client(), every: 20 * time.Millisecond}
	d.url = srv.URL
	return d, got
}

func discordNext(t *testing.T, got chan map[string]any) map[string]any {
	select {
	case m := <-got:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("nothing posted")
		return nil
	}
}

func TestDiscordSpeakers(t *testing.T) {
	d, got := discordFixture(t)
	d.setGamerpic(func(context.Context, string) (string, error) { return "https://pic/x.png", nil })
	d.servers = []string{"PVP"}
	d.add("skygen", "not sent", "Eve", "", "not sent")
	d.add("pvp", "Rank Ann: one", "Ann", "123", "one")
	d.add("pvp", "Rank Ann: two", "Ann", "123", "two")
	d.add("pvp", "Bob: three", "Bob", "", "three")
	d.add("pvp", "plain line", "", "", "")
	d.set(d.url, d.servers)

	m := discordNext(t, got)
	if m["username"] != "Ann" || m["avatar_url"] != "https://pic/x.png" {
		t.Fatalf("speaker: %v", m)
	}
	if s, _ := m["content"].(string); !strings.Contains(s, "one") || !strings.Contains(s, "two") || strings.Contains(s, "Rank") {
		t.Fatalf("content %q", m["content"])
	}
	if p, ok := m["allowed_mentions"].(map[string]any)["parse"].([]any); !ok || len(p) != 0 {
		t.Fatalf("mentions allowed: %v", m["allowed_mentions"])
	}
	m = discordNext(t, got)
	if m["username"] != "Bob" || !strings.Contains(m["avatar_url"].(string), "/Bob/") {
		t.Fatalf("java speaker: %v", m)
	}
	m = discordNext(t, got)
	if _, ok := m["username"]; ok || !strings.Contains(m["content"].(string), "plain line") {
		t.Fatalf("plain line: %v", m)
	}
}

func TestDiscordOff(t *testing.T) {
	d := &discordBridge{lines: make(chan discordEntry, 1)}
	d.add("pvp", "x", "", "", "")
	if len(d.lines) != 0 {
		t.Fatal("queued with no webhook")
	}
}
