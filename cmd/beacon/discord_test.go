package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiscordLine(t *testing.T) {
	got := discordLine("pvp", "§c<gradient:1:m>Owner</gradient> §fbob_1: *hi* @everyone")
	want := `**[pvp]** Owner bob\_1: \*hi\* @everyone`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDiscordSendsChosenServers(t *testing.T) {
	got := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got <- m
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := &discordBridge{lines: make(chan string, 8), client: srv.Client(), every: 20 * time.Millisecond}
	d.set(srv.URL, []string{"PVP"})
	d.add("skygen", "not sent")
	d.add("pvp", "one")
	d.add("pvp", "two")

	select {
	case m := <-got:
		s, _ := m["content"].(string)
		if !strings.Contains(s, "one") || strings.Contains(s, "not sent") {
			t.Fatalf("content %q", s)
		}
		am, _ := m["allowed_mentions"].(map[string]any)
		if p, ok := am["parse"].([]any); !ok || len(p) != 0 {
			t.Fatalf("mentions allowed: %v", m["allowed_mentions"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing posted")
	}
}

func TestDiscordOff(t *testing.T) {
	d := &discordBridge{lines: make(chan string, 1)}
	d.add("pvp", "x")
	if len(d.lines) != 0 {
		t.Fatal("queued with no webhook")
	}
}
