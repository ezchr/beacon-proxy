package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/ezchr/beacon-proxy/internal/xbl"
)

func TestNeedsNewAtOneFreePlace(t *testing.T) {
	for _, c := range []struct {
		members, cap int
		want         bool
	}{{1, 30, false}, {28, 30, false}, {29, 30, true}, {30, 30, true}, {1, 3, false}, {2, 3, true}} {
		if got := needsNew(c.members, c.cap); got != c.want {
			t.Errorf("needsNew(%d, %d) = %v, want %v", c.members, c.cap, got, c.want)
		}
	}
}

// Invites and the host's entry follow the newest session; rebuilds get the later sessions' ids.
func TestSessionSetCurrentIsNewest(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := xbl.New(nil, "1", "", "c", "p", 1, log)
	b := xbl.New(nil, "1", "", "c", "p", 1, log)
	s := &sessionSet{broadcast: "t-current", list: []*xbl.Session{a}, log: log}
	if s.current() != a {
		t.Fatal("one session: not current")
	}
	s.list = append(s.list, b)
	s.rememberIDs()
	if s.current() != b {
		t.Fatal("the newest session is not current")
	}
	v, _ := rolloverIDs.Load("t-current")
	if ids := v.([]string); len(ids) != 1 || ids[0] != b.SessionID() {
		t.Fatalf("remembered ids %v", ids)
	}
	if a.Members() != 1 {
		t.Fatalf("a new session counts %d members, want 1 (the host)", a.Members())
	}
}
