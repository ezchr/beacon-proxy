package main

import (
	"context"
	"log/slog"
	"testing"
)

// Members join and show the world; inviters join and invite in turn, inviter being one more.
func TestStartSessionMembers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // the members only wait for their accounts, which never sign in here
	cfg := FileConfig{Members: []string{"a", "b"}, Inviters: []string{"b", "c"}, Inviter: "a"}
	pool := startSessionMembers(ctx, cfg, "host", nil, slog.New(slog.DiscardHandler))
	if pool == nil || pool.Parallel() != 3 {
		t.Fatalf("pool %+v", pool)
	}
	want := map[string]bool{"b": true, "c": false, "a": true}
	for i, n := range []string{"b", "c", "a"} {
		m := pool.members[i]
		if m.name != n || m.activity != want[n] {
			t.Errorf("inviter %d: %s activity %v, want %s %v", i, m.name, m.activity, n, want[n])
		}
	}
	if p := startSessionMembers(ctx, FileConfig{Members: []string{"a"}}, "host", nil, slog.New(slog.DiscardHandler)); p != nil {
		t.Error("members without inviters gave an inviter pool")
	}
}
