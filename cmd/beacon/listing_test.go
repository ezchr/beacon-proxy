package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListingGateWorlds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "listing_state.json")
	g := newListingGate(path, []string{mainWorld, "skygen"}, slog.Default())
	if !g.On() || !g.WorldOn(primaryBroadcast) || !g.WorldOn("skygen") {
		t.Fatal("a new gate should have every world listed")
	}
	if err := g.SetWorld("Skygen", false); err != nil {
		t.Fatal(err)
	}
	if !g.WorldOn("main") || g.WorldOn("skygen") || !g.On() {
		t.Fatal("switching skygen off touched another world")
	}
	if err := g.SetWorld("nope", false); err == nil {
		t.Fatal("an unknown world should be an error")
	}
	if err := g.Set(false); err != nil {
		t.Fatal(err)
	}
	if g.On() {
		t.Fatal("Set(false) should switch every world off")
	}
	// The state survives a restart.
	g2 := newListingGate(path, []string{mainWorld, "skygen"}, slog.Default())
	if g2.WorldOn("main") || g2.WorldOn("skygen") {
		t.Fatal("state was not kept")
	}
}

func TestListingGateOldStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "listing_state.json")
	if err := os.WriteFile(path, []byte(`{"on":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g := newListingGate(path, []string{mainWorld, "skygen"}, slog.Default())
	if g.On() {
		t.Fatal(`{"on":false} from before should mean every world off`)
	}
	if err := os.WriteFile(path, []byte(`{"on":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !newListingGate(path, []string{mainWorld, "skygen"}, slog.Default()).On() {
		t.Fatal(`{"on":true} should mean every world on`)
	}
}

func TestListingGateWait(t *testing.T) {
	g := newListingGate(filepath.Join(t.TempDir(), "s.json"), []string{mainWorld, "skygen"}, slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	offed := make(chan error, 1)
	go func() { offed <- g.waitOff(ctx, "skygen") }()
	_ = g.SetWorld("main", false) // another world: skygen's waiter keeps waiting
	select {
	case <-offed:
		t.Fatal("waitOff returned for another world's switch")
	case <-time.After(50 * time.Millisecond):
	}
	_ = g.SetWorld("skygen", false)
	if err := <-offed; err != nil {
		t.Fatal(err)
	}
	ond := make(chan error, 1)
	go func() { ond <- g.waitOn(ctx, "skygen") }()
	_ = g.Set(true)
	if err := <-ond; err != nil {
		t.Fatal(err)
	}
}
