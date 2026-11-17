package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"

	"github.com/ezchr/beacon-proxy/internal/session"
	"github.com/ezchr/beacon-proxy/internal/xbl"
	"github.com/google/uuid"
)

// sessionCap is how many members (the host included) one Friends-tab session holds: Xbox's
// MinecraftLobby template allows 30. BEACON_SESSION_CAP lowers it, for testing rollover.
func sessionCap() int {
	if n, err := strconv.Atoi(os.Getenv("BEACON_SESSION_CAP")); err == nil && n >= 2 {
		return n
	}
	return 30
}

// sessionInviter sends Xbox invites into a world: the invite controllers use the newest session.
type sessionInviter interface {
	SendInvite(ctx context.Context, xuid string) error
}

// rolloverIDs keeps, by broadcast name, the ids of the sessions a broadcast opened after its first,
// so a session rebuild (token renewal, signaling reconnect) puts the players in them back in the
// same session documents.
var rolloverIDs sync.Map // broadcast name -> []string

// sessionSet is one broadcast's Friends-tab sessions. A session holds sessionCap members; when the
// newest has one free place left, a new one opens and the host's Friends-tab entry (its activity
// handle, which Xbox keeps one of per account) moves to it. Players in older sessions keep showing
// those to their friends, so every session stays reachable, and a session only the host is left in
// closes. All of them share one NetherNet listener: they are the same world.
type sessionSet struct {
	auth        *session.Session
	xuid        string
	pmsgID      string
	netherNetID uint64
	broadcast   string
	cap         int
	worldInfo   func() xbl.Config
	log         *slog.Logger

	mu      sync.Mutex
	list    []*xbl.Session // oldest first; the last is the one the host's entry points at
	opening bool
}

// current is the newest session: the one new players find through the host, and invites use.
func (s *sessionSet) current() *xbl.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list[len(s.list)-1]
}

// all is every open session, oldest first.
func (s *sessionSet) all() []*xbl.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*xbl.Session(nil), s.list...)
}

// SendInvite invites xuid into the newest session.
func (s *sessionSet) SendInvite(ctx context.Context, xuid string) error {
	return s.current().SendInvite(ctx, xuid)
}

// rememberIDs stores the ids of the sessions after the first for the next rebuild. s.mu held.
func (s *sessionSet) rememberIDs() {
	ids := make([]string, 0, len(s.list)-1)
	for _, x := range s.list[1:] {
		ids = append(ids, x.SessionID())
	}
	rolloverIDs.Store(s.broadcast, ids)
}

// newSession makes a session object for id ("" for a new one).
func (s *sessionSet) newSession(id, connectionID string) *xbl.Session {
	if id == "" {
		id = uuid.NewString()
	}
	x := xbl.New(s.auth, s.xuid, id, connectionID, s.pmsgID, s.netherNetID, s.log)
	x.SetWorldInfo(s.worldInfo())
	return x
}

// restore creates again the sessions this broadcast had open after its first before a rebuild, in
// their order, so the host's entry ends on the newest. One that can't be created is dropped.
func (s *sessionSet) restore(ctx context.Context, connectionID string) {
	v, ok := rolloverIDs.Load(s.broadcast)
	if !ok {
		return
	}
	for _, id := range v.([]string) {
		x := s.newSession(id, connectionID)
		if err := x.Create(ctx); err != nil {
			s.log.Warn("could not open a rolled-over session again, its players stay connected", "session", id, "err", err)
			continue
		}
		s.mu.Lock()
		s.list = append(s.list, x)
		s.mu.Unlock()
		s.log.Info("rolled-over session open again", "session", id)
	}
	s.mu.Lock()
	s.rememberIDs()
	s.mu.Unlock()
}

// needsNew reports whether a session with members members (the host included) is full enough that
// a new one should open: one free place left.
func needsNew(members, cap int) bool { return members >= cap-1 }

// check runs after the members of the sessions were read (RefreshNonces): it opens a new session
// when the newest is nearly full, and closes older ones only the host is left in.
func (s *sessionSet) check(ctx context.Context, connectionID string) {
	s.mu.Lock()
	newest := s.list[len(s.list)-1]
	var drained []*xbl.Session
	keep := s.list[:0:0]
	for i, x := range s.list {
		// The first session stays: rebuilds keep its id, and it is where a fresh start begins.
		if i > 0 && x != newest && x.Members() == 1 {
			drained = append(drained, x)
			continue
		}
		keep = append(keep, x)
	}
	if len(drained) > 0 {
		s.list = keep
		s.rememberIDs()
	}
	open := !s.opening && needsNew(newest.Members(), s.cap)
	if open {
		s.opening = true
	}
	s.mu.Unlock()

	for _, x := range drained {
		if err := x.Leave(ctx); err != nil {
			s.log.Warn("could not close an emptied session", "session", x.SessionID(), "err", err)
		} else {
			s.log.Info("closed a session everyone has left", "session", x.SessionID())
		}
	}
	if !open {
		return
	}
	defer func() {
		s.mu.Lock()
		s.opening = false
		s.mu.Unlock()
	}()
	x := s.newSession("", connectionID)
	if err := x.Create(ctx); err != nil {
		s.log.Error("the Friends-tab session is nearly full and a new one could not open", "members", newest.Members(), "err", err)
		return
	}
	s.mu.Lock()
	s.list = append(s.list, x)
	s.rememberIDs()
	n := len(s.list)
	s.mu.Unlock()
	s.log.Info(fmt.Sprintf("the Friends-tab session is nearly full (%d of %d): a new one opened, new players and invites go there", newest.Members(), s.cap),
		"session", x.SessionID(), "sessions", n)
}
