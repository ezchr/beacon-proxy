package bridge

import (
	"errors"
	"io"
	"net"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// A server going down holds its players; a server letting one player go does not.
func TestHoldableReasons(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, true},
		{io.EOF, true},
		{net.ErrClosed, true},
		{errors.New("read batch: read tcp 127.0.0.1:1->127.0.0.1:2: read: connection reset by peer"), true},
		{&kickError{msg: "§rThe server restarted!\nJoin back in a few seconds."}, true},
		{&kickError{msg: "§rThe server closed. Join back in a few seconds."}, true},
		{&kickError{msg: "You were kicked from skygen: Server closed"}, true},
		{&kickError{msg: "%disconnect.disconnected"}, true},
		{&kickError{msg: "Server is shutting down"}, true},
		{&kickError{msg: "You are banned: griefing"}, false},
		{&kickError{msg: "Kicked by an operator."}, false},
		{&kickError{msg: "You were kicked from skygen: Timed out"}, false},
		{&kickError{msg: "Already logged in."}, false},
		{&kickError{msg: ""}, false},
	}
	for _, c := range cases {
		if got := holdable(c.err, nil); got != c.want {
			t.Errorf("holdable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	if !holdable(&kickError{msg: "See you SOON, maintenance"}, []string{" see you soon "}) {
		t.Error("a server's own hold-reasons were not used")
	}
	if holdable(&kickError{msg: "You are banned"}, []string{""}) {
		t.Error("an empty hold-reason matched every kick")
	}
}

// A new hold gets the full time; a server that falls over again during or right after the reattach
// that ended the last hold only gets what was left of it.
func TestHoldDeadline(t *testing.T) {
	now := time.Now()
	hold := 90 * time.Second
	if got := holdDeadline(endedHold{}, "kits", now, hold); !got.Equal(now.Add(hold)) {
		t.Fatalf("first hold: %v", got.Sub(now))
	}
	old := now.Add(20 * time.Second)
	recent := endedHold{server: "kits", deadline: old, ended: now.Add(-10 * time.Second), settled: true}
	if got := holdDeadline(recent, "KITS", now, hold); !got.Equal(old) {
		t.Fatalf("down again 10s after coming back: got %v, want the old deadline", got.Sub(now))
	}
	unsettled := endedHold{server: "kits", deadline: old, ended: now.Add(-5 * time.Minute)}
	if got := holdDeadline(unsettled, "kits", now, hold); !got.Equal(old) {
		t.Fatalf("down again before the player was spawned: got %v, want the old deadline", got.Sub(now))
	}
	long := endedHold{server: "kits", deadline: old, ended: now.Add(-5 * time.Minute), settled: true}
	if got := holdDeadline(long, "kits", now, hold); !got.Equal(now.Add(hold)) {
		t.Fatalf("a restart long after the last one: %v", got.Sub(now))
	}
	other := endedHold{server: "pvp", deadline: old, ended: now, settled: false}
	if got := holdDeadline(other, "kits", now, hold); !got.Equal(now.Add(hold)) {
		t.Fatalf("another server's hold: %v", got.Sub(now))
	}
}

// fakeServer is the server a group of fake held players waits for.
type fakeServer struct {
	up       atomic.Bool
	probes   atomic.Int32
	login    time.Duration // how long a login takes
	failFor  int32         // logins fail while probes <= failFor
	active   atomic.Int32
	max      atomic.Int32
	attempts atomic.Int32

	mu     sync.Mutex
	starts []time.Time
}

func (f *fakeServer) probe() bool {
	f.probes.Add(1)
	return f.up.Load()
}

type fakeHeld struct {
	srv *fakeServer

	mu         sync.Mutex
	held       bool
	busy       int // reattach answers holdBusy this many times first
	reattached int
	reminded   int
	expired    int
}

func (p *fakeHeld) stillHeld() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held
}

func (p *fakeHeld) remind() {
	p.mu.Lock()
	p.reminded++
	p.mu.Unlock()
}

func (p *fakeHeld) expire() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held {
		p.held = false
		p.expired++
	}
}

func (p *fakeHeld) reattach() holdResult {
	p.mu.Lock()
	if p.busy > 0 {
		p.busy--
		p.mu.Unlock()
		return holdBusy
	}
	p.mu.Unlock()
	f := p.srv
	f.attempts.Add(1)
	f.mu.Lock()
	f.starts = append(f.starts, time.Now())
	f.mu.Unlock()
	n := f.active.Add(1)
	for {
		m := f.max.Load()
		if n <= m || f.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(f.login)
	f.active.Add(-1)
	if !f.up.Load() || f.probes.Load() <= f.failFor {
		return holdRetry
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = false
	p.reattached++
	return holdDone
}

// startGroup runs a group of n fake players held until deadline; done is closed when its goroutine
// ended.
func startGroup(srv *fakeServer, n int, deadline time.Time, interval time.Duration) (*holdGroup, []*fakeHeld, chan struct{}) {
	g := &holdGroup{probe: srv.probe, interval: interval, parallel: 4, gap: 5 * time.Millisecond}
	var players []*fakeHeld
	for range n {
		p := &fakeHeld{srv: srv, held: true}
		players = append(players, p)
		g.members = append(g.members, &holdMember{p: p, deadline: deadline})
	}
	done := make(chan struct{})
	go func() {
		g.run(func() bool {
			g.mu.Lock()
			defer g.mu.Unlock()
			return len(g.members) == 0
		})
		close(done)
	}()
	return g, players, done
}

func waitDone(t *testing.T, done chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("the hold group did not end")
	}
}

// Fifty players held while the server is down all come back once it is up: each exactly once, never
// more than four logging in at once, the logins started apart, and nothing left running after.
func TestHoldGroupBringsManyBack(t *testing.T) {
	before := runtime.NumGoroutine()
	srv := &fakeServer{login: 10 * time.Millisecond}
	_, players, done := startGroup(srv, 50, time.Now().Add(time.Minute), 20*time.Millisecond)
	time.Sleep(120 * time.Millisecond) // down for a few probes
	if a := srv.attempts.Load(); a > 1 {
		t.Fatalf("%d logins tried while the server was down (one forced try at most)", a)
	}
	srv.up.Store(true)
	waitDone(t, done, 10*time.Second)
	for i, p := range players {
		if p.reattached != 1 || p.expired != 0 || p.held {
			t.Fatalf("player %d: reattached %d, expired %d, held %v", i, p.reattached, p.expired, p.held)
		}
		if p.reminded == 0 {
			t.Fatalf("player %d was never shown the hold title", i)
		}
	}
	if m := srv.max.Load(); m > 4 {
		t.Fatalf("%d logins at once", m)
	}
	// Started the gap apart: 50 logins span at least 49 gaps.
	sort.Slice(srv.starts, func(i, j int) bool { return srv.starts[i].Before(srv.starts[j]) })
	if span := srv.starts[len(srv.starts)-1].Sub(srv.starts[0]); span < 49*5*time.Millisecond*9/10 {
		t.Fatalf("50 logins started within %v: not staggered", span)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g := runtime.NumGoroutine(); g > before {
		t.Fatalf("%d goroutines left over (%d before)", g, before)
	}
}

// A server not back within the hold time moves its players on, once each, and the group ends.
func TestHoldGroupExpires(t *testing.T) {
	srv := &fakeServer{login: time.Millisecond}
	_, players, done := startGroup(srv, 5, time.Now().Add(80*time.Millisecond), 30*time.Millisecond)
	waitDone(t, done, 5*time.Second)
	time.Sleep(20 * time.Millisecond) // expire runs on its own goroutine
	for i, p := range players {
		p.mu.Lock()
		if p.expired != 1 || p.reattached != 0 {
			t.Fatalf("player %d: expired %d, reattached %d", i, p.expired, p.reattached)
		}
		p.mu.Unlock()
	}
}

// A player who leaves (disconnects, or moves elsewhere with /server) while held is just dropped.
func TestHoldGroupPlayerLeaves(t *testing.T) {
	srv := &fakeServer{login: time.Millisecond}
	_, players, done := startGroup(srv, 3, time.Now().Add(time.Minute), 20*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	for _, p := range players {
		p.mu.Lock()
		p.held = false
		p.mu.Unlock()
	}
	waitDone(t, done, 5*time.Second)
	for i, p := range players {
		if p.expired != 0 || p.reattached != 0 {
			t.Fatalf("player %d: expired %d, reattached %d", i, p.expired, p.reattached)
		}
	}
}

// A server that answers pings but refuses logins (still starting) gets a few tries per round, not
// one per held player.
func TestHoldGroupServerNotReady(t *testing.T) {
	srv := &fakeServer{login: 2 * time.Millisecond, failFor: 3}
	srv.up.Store(true)
	_, players, done := startGroup(srv, 20, time.Now().Add(time.Minute), 20*time.Millisecond)
	waitDone(t, done, 10*time.Second)
	for i, p := range players {
		if p.reattached != 1 {
			t.Fatalf("player %d reattached %d times", i, p.reattached)
		}
	}
	// 3 failing rounds of at most 4 tries each, then 20 good ones.
	if a := srv.attempts.Load(); a > 3*4+20 {
		t.Fatalf("%d logins tried, want at most %d", a, 3*4+20)
	}
	if p := srv.probes.Load(); p < 4 {
		t.Fatalf("back after %d probes, want the 3 failing rounds first", p)
	}
}

// Players busy with a move of their own are tried again later and don't stop the others.
func TestHoldGroupBusyPlayers(t *testing.T) {
	srv := &fakeServer{login: time.Millisecond}
	srv.up.Store(true)
	_, players, done := startGroup(srv, 8, time.Now().Add(time.Minute), 20*time.Millisecond)
	for _, p := range players[:6] {
		p.mu.Lock()
		p.busy = 2
		p.mu.Unlock()
	}
	waitDone(t, done, 5*time.Second)
	for i, p := range players {
		if p.reattached != 1 {
			t.Fatalf("player %d reattached %d times", i, p.reattached)
		}
	}
}

// joinHold keeps one group per server and network, counted by heldOn, and drops it once empty.
func TestHoldRegistry(t *testing.T) {
	n := &Network{}
	srv := &fakeServer{}
	key := holdKey{n, "kits"}
	mk := func() *holdGroup {
		return &holdGroup{probe: srv.probe, interval: 10 * time.Millisecond, parallel: 4, gap: time.Millisecond}
	}
	a, b := &fakeHeld{srv: srv, held: true}, &fakeHeld{srv: srv, held: true}
	joinHold(key, &holdMember{p: a, deadline: time.Now().Add(time.Minute)}, mk)
	joinHold(key, &holdMember{p: b, deadline: time.Now().Add(time.Minute)}, mk)
	if got := heldOn(n, "KITS"); got != 2 {
		t.Fatalf("heldOn = %d, want 2", got)
	}
	if got := heldOn(n, "pvp"); got != 0 {
		t.Fatalf("heldOn another server = %d", got)
	}
	holds.mu.Lock()
	groups := len(holds.groups)
	holds.mu.Unlock()
	if groups != 1 {
		t.Fatalf("%d groups for one server", groups)
	}
	srv.up.Store(true)
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		holds.mu.Lock()
		_, ok := holds.groups[key]
		holds.mu.Unlock()
		if !ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	holds.mu.Lock()
	_, ok := holds.groups[key]
	holds.mu.Unlock()
	if ok || a.reattached != 1 || b.reattached != 1 {
		t.Fatalf("group left: %v, reattached %d %d", ok, a.reattached, b.reattached)
	}
}

func testSession() *switchSession {
	return &switchSession{
		clientRID: 1, clientUID: 1,
		entities: map[int64]bool{}, players: map[uuid.UUID]bool{}, bossBars: map[int64]bool{}, waypoints: map[uuid.UUID]bool{},
		objectives: map[string]bool{}, scores: map[int64]protocol.ScoreboardEntry{}, links: map[int64]int64{},
		containers: map[byte]byte{}, hiddenHud: map[int32]bool{},
	}
}

// What the old session showed is tracked and all taken away when the player is held: entities
// (not the player itself), the player list, boss bars, scoreboards, open windows, and the effects.
func TestHoldClearsTheOldWorld(t *testing.T) {
	s := testSession()
	other := uuid.New()
	for _, pk := range []packet.Packet{
		&packet.AddActor{EntityUniqueID: 50, EntityRuntimeID: 50, EntityType: "minecraft:zombie"},
		&packet.AddItemActor{EntityUniqueID: 51, EntityRuntimeID: 51},
		&packet.AddPlayer{UUID: other, EntityRuntimeID: 52, AbilityData: protocol.AbilityData{EntityUniqueID: 52}},
		&packet.AddActor{EntityUniqueID: 53, EntityRuntimeID: 53, EntityType: "minecraft:pig"},
		&packet.RemoveActor{EntityUniqueID: 53},
		&packet.PlayerList{Entries: []protocol.PlayerListEntry{{ActionType: protocol.PlayerListActionAdd, UUID: other}}},
		&packet.BossEvent{BossEntityUniqueID: 60, EventType: packet.BossEventShow},
		&packet.SetDisplayObjective{ObjectiveName: "kills", DisplaySlot: "sidebar"},
		&packet.SetScore{Entries: []protocol.ScoreboardEntry{{EntryID: 7, ObjectiveName: "kills", IdentityType: protocol.ScoreboardIdentityFakePlayer, DisplayName: "x"}}},
		&packet.ContainerOpen{WindowID: 3, ContainerType: 0},
	} {
		s.track(encodePacket(pk))
	}
	out := s.clearClientLocked()
	removed := map[int64]bool{}
	var listRemoved, barHidden, objRemoved, scoreRemoved, closed bool
	for _, pk := range out {
		switch pk := pk.(type) {
		case *packet.RemoveActor:
			removed[pk.EntityUniqueID] = true
		case *packet.PlayerList:
			listRemoved = len(pk.Entries) == 1 && pk.Entries[0].UUID == other && pk.Entries[0].ActionType == protocol.PlayerListActionRemove
		case *packet.BossEvent:
			barHidden = pk.BossEntityUniqueID == 60 && pk.EventType == packet.BossEventHide
		case *packet.RemoveObjective:
			objRemoved = pk.ObjectiveName == "kills"
		case *packet.SetScore:
			scoreRemoved = len(pk.Entries) == 1 && pk.Entries[0].EntryID == 7
		case *packet.ContainerClose:
			closed = pk.WindowID == 3 && pk.ServerSide
		}
	}
	if len(removed) != 3 || !removed[50] || !removed[51] || !removed[52] {
		t.Fatalf("entities removed: %v, want 50, 51, 52", removed)
	}
	if !listRemoved || !barHidden || !objRemoved || !scoreRemoved || !closed {
		t.Fatalf("list %v, bar %v, objective %v, score %v, window %v", listRemoved, barHidden, objRemoved, scoreRemoved, closed)
	}
	if len(s.entities)+len(s.players)+len(s.bossBars)+len(s.objectives)+len(s.scores)+len(s.containers) != 0 {
		t.Fatal("the bookkeeping was not emptied")
	}
	// The player's own entity is never removed.
	s.entities[s.clientUID] = true
	for _, pk := range s.clearClientLocked() {
		if r, ok := pk.(*packet.RemoveActor); ok && r.EntityUniqueID == s.clientUID {
			t.Fatal("the player's own entity was removed")
		}
	}
	// A second clear (the reattach) has nothing left to remove.
	for _, pk := range s.clearClientLocked() {
		if _, ok := pk.(*packet.RemoveActor); ok {
			t.Fatal("removed an entity twice")
		}
	}
}

// The restarted server numbers the player's own entity anew: the reattach maps it onto the one the
// client has known itself by since it joined, both ways, and an entity of the server that takes the
// client's number gets the server's own number for the player instead.
func TestReattachMapsTheServersPlayerID(t *testing.T) {
	const clientRID, serverRID = 1, 77
	r := newRewriter(serverRID, serverRID, clientRID, clientRID, nil)
	pool := packet.NewClientPool()
	for id, f := range packet.NewServerPool() {
		pool[id] = f
	}
	rw := func(pk packet.Packet, toClient bool) packet.Packet {
		t.Helper()
		out, err := r.rewrite(encodePacket(pk), toClient, pool)
		if err != nil {
			t.Fatal(err)
		}
		id, payload, err := splitPacket(out)
		if err != nil {
			t.Fatal(err)
		}
		got := pool[id]()
		if err := decodeInto(got, payload); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := rw(&packet.SetActorData{EntityRuntimeID: serverRID}, true).(*packet.SetActorData); got.EntityRuntimeID != clientRID {
		t.Fatalf("server's player id %d reached the client as %d, want %d", serverRID, got.EntityRuntimeID, clientRID)
	}
	if got := rw(&packet.SetActorData{EntityRuntimeID: clientRID}, true).(*packet.SetActorData); got.EntityRuntimeID != serverRID {
		t.Fatalf("server entity %d reached the client as %d, want %d", clientRID, got.EntityRuntimeID, serverRID)
	}
	if got := rw(&packet.PlayerAction{EntityRuntimeID: clientRID, ActionType: protocol.PlayerActionStartSneak}, false).(*packet.PlayerAction); got.EntityRuntimeID != serverRID {
		t.Fatalf("client's own id reached the server as %d, want %d", got.EntityRuntimeID, serverRID)
	}
}
