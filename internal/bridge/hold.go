package bridge

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/text"
)

// Holding players while their server restarts (hold = "90s" for a server in beacon.toml).
//
// Without a hold, a server going down sends its players to another server (backendGone). With
// one, a server that goes down (it closed every connection for a restart, or its process died)
// keeps its players on Beacon: their client stays connected and in the world they were in, frozen
// in place, with a title saying the server is restarting. Once the server answers again each one
// is logged in to it again, as at a move, and put back without a dimension change: the client's
// world is the same server's world, so only what the old session showed (entities, players, boss
// bars, scoreboards, open windows, effects) is taken away, the new StartGame's settings applied
// and the player put at its position. The server's chunks then replace the ones the client kept.
// A server not back within the hold time sends them on as before.
//
// A server that lets one player go on purpose (a kick, a ban) is not held: only a link that ended
// without a reason (the connection closed or broke) or with a restart message (holdRestartWords
// and the server's own hold-reasons) is.
//
// One holdGroup per server does the waiting for all of its held players: it reminds them every
// holdInterval, probes the server, and once it answers logs them in again holdParallel at a time,
// holdGap apart, so forty players coming back do not all knock at once.

const (
	// defaultHoldMessage is the title held players see, unless the server sets hold-message.
	defaultHoldMessage = "Server restarting, hold on..."
	// holdInterval is how often a held player is reminded and the server probed.
	holdInterval = 2 * time.Second
	// holdParallel is how many held players log in again at once.
	holdParallel = 4
	// holdGap is the least time between two held players starting to log in again.
	holdGap = 150 * time.Millisecond
	// holdLoginTimeout is the longest one try at logging a held player in again waits for StartGame.
	holdLoginTimeout = 15 * time.Second
	// holdForceEvery: a server whose probe keeps failing still gets one login try this many probes
	// apart (a server that answers logins but not pings).
	holdForceEvery = 5
	// holdFlapWindow: a hold that starts this soon after the last one on the same server ended in
	// a reattach goes on with that hold's time instead of a new one, so a server that keeps
	// falling over moves its players on in the end.
	holdFlapWindow = 30 * time.Second
	// MaxHold is the longest hold a server may set.
	MaxHold = 10 * time.Minute
)

// holdRestartWords are the parts of kick messages (any case, colours removed) that mean the server
// is going down rather than letting the player go: Dragonfly's default and Crossfly's shutdown
// messages, and Velocity's and Geyser's when the server behind them stops.
var holdRestartWords = []string{
	"restart",
	"server closed",
	"server is closing",
	"shutting down",
	"server stopped",
	"%disconnect.disconnected",
	"disconnectionscreen.serverclosed",
}

// holdable reports whether a link that ended with err is the server going down (a hold applies)
// rather than the server letting this one player go (a kick: shown to them as before).
func holdable(err error, extra []string) bool {
	var kick *kickError
	if !errors.As(err, &kick) {
		return true // no reason given: the connection closed or broke, the server or its process went away
	}
	msg := strings.ToLower(text.Clean(kick.msg))
	for _, w := range holdRestartWords {
		if strings.Contains(msg, w) {
			return true
		}
	}
	for _, w := range extra {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// holdState is one player's wait for their server. Only the switchSession's s.hold pointing at it
// makes it current; whatever ends the hold (the reattach, a move, the hold time running out) sets
// s.hold to nil under s.mu.
type holdState struct {
	server   string
	since    time.Time
	deadline time.Time
	reason   string // why the server let them go, shown if they are moved on in the end
	message  string
	tries    atomic.Int32
}

// endedHold is the last hold of a player that ended in a reattach (see holdFlapWindow).
type endedHold struct {
	server   string
	deadline time.Time
	ended    time.Time
	// settled is set once the reattached server spawned the player.
	settled bool
}

// holdDeadline is when a hold for server starting now ends: hold from now, or the time left of the
// last hold if the server fell over again during or soon after the reattach that ended it.
func holdDeadline(last endedHold, server string, now time.Time, hold time.Duration) time.Time {
	if last.server != "" && strings.EqualFold(last.server, server) && (!last.settled || now.Sub(last.ended) < holdFlapWindow) {
		return last.deadline
	}
	return now.Add(hold)
}

// holdResult is the outcome of one try at putting a held player back on their server.
type holdResult int

const (
	holdRetry holdResult = iota // not back yet: try again later
	holdDone                    // the hold is over (reattached, moved on, or gone)
	holdBusy                    // the player is in the middle of something else: try again later
)

// heldPlayer is what a holdGroup does with a held player (a switchSession; a fake in tests).
type heldPlayer interface {
	// stillHeld reports whether the player still waits (not moved, reattached or gone).
	stillHeld() bool
	// remind shows the hold title again.
	remind()
	// reattach tries once to log the player in to the server again and put them back on it.
	reattach() holdResult
	// expire ends a hold whose time ran out: the player is moved on as without a hold.
	expire()
}

type holdMember struct {
	p        heldPlayer
	deadline time.Time
}

// holdKey names a holdGroup: one per server of each Network.
type holdKey struct {
	n      *Network
	server string // lower case
}

// holdGroup waits for one server for all the players held for it. Its goroutine runs while it has
// members.
type holdGroup struct {
	key      holdKey
	probe    func() bool // the server answers; false also when it is no longer configured
	interval time.Duration
	parallel int
	gap      time.Duration

	mu      sync.Mutex
	members []*holdMember
}

var holds = struct {
	mu     sync.Mutex
	groups map[holdKey]*holdGroup
}{groups: map[holdKey]*holdGroup{}}

// joinHold adds a held player to their server's group, starting the group if it has none.
func joinHold(key holdKey, m *holdMember, newGroup func() *holdGroup) {
	holds.mu.Lock()
	defer holds.mu.Unlock()
	g, ok := holds.groups[key]
	if !ok {
		g = newGroup()
		g.key = key
		holds.groups[key] = g
	}
	g.mu.Lock()
	g.members = append(g.members, m)
	g.mu.Unlock()
	if !ok {
		go g.run(func() bool { return leaveHolds(g) })
	}
}

// leaveHolds removes an empty group from the registry; it reports whether it did (the group's
// goroutine then ends). A player joining meanwhile keeps the group going.
func leaveHolds(g *holdGroup) bool {
	holds.mu.Lock()
	defer holds.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.members) > 0 {
		return false
	}
	if holds.groups[g.key] == g {
		delete(holds.groups, g.key)
	}
	return true
}

// heldOn is how many players n holds for server right now.
func heldOn(n *Network, server string) int {
	holds.mu.Lock()
	g := holds.groups[holdKey{n, strings.ToLower(server)}]
	holds.mu.Unlock()
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.members)
}

// run reminds the members, probes the server and brings the members back, every interval, until
// the group is empty and exit agrees.
func (g *holdGroup) run(exit func() bool) {
	tick := time.NewTicker(g.interval)
	defer tick.Stop()
	misses := 0
	for range tick.C {
		live := g.prune(time.Now())
		if len(live) == 0 {
			if exit() {
				return
			}
			continue
		}
		for _, m := range live {
			m.p.remind()
		}
		if !g.probe() {
			misses++
			if misses%holdForceEvery != 0 {
				continue
			}
			live = live[:1]
		} else {
			misses = 0
		}
		g.round(live)
	}
}

// prune drops the members no longer held, ends the holds whose time ran out, and returns the rest.
func (g *holdGroup) prune(now time.Time) []*holdMember {
	g.mu.Lock()
	defer g.mu.Unlock()
	kept := g.members[:0]
	var live []*holdMember
	for _, m := range g.members {
		switch {
		case !m.p.stillHeld():
		case now.After(m.deadline):
			go m.p.expire()
		default:
			kept = append(kept, m)
			live = append(live, m)
		}
	}
	clear(g.members[len(kept):])
	g.members = kept
	return live
}

func (g *holdGroup) remove(m *holdMember) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, x := range g.members {
		if x == m {
			g.members = append(g.members[:i], g.members[i+1:]...)
			return
		}
	}
}

// round tries to bring the members back, parallel at a time and gap apart. Until one try worked, at
// most parallel tries are made: if they all failed the server is not ready yet (it answers pings
// but no logins while it starts), and the rest wait for the next round.
func (g *holdGroup) round(live []*holdMember) {
	sem := make(chan struct{}, g.parallel)
	var (
		wg                         sync.WaitGroup
		mu                         sync.Mutex
		launched, busy, fails, oks int
	)
	for i, m := range live {
		mu.Lock()
		unsure := oks == 0 && launched-busy >= g.parallel
		mu.Unlock()
		if unsure {
			wg.Wait() // what the first tries say decides whether to go on
			mu.Lock()
			stop := oks == 0 && fails >= g.parallel
			mu.Unlock()
			if stop {
				break
			}
		}
		if i > 0 {
			time.Sleep(g.gap)
		}
		sem <- struct{}{}
		if !m.p.stillHeld() || time.Now().After(m.deadline) {
			<-sem
			continue // pruned on the next tick
		}
		mu.Lock()
		launched++
		mu.Unlock()
		wg.Add(1)
		go func(m *holdMember) {
			defer wg.Done()
			defer func() { <-sem }()
			r := m.p.reattach()
			mu.Lock()
			switch r {
			case holdDone:
				oks++
			case holdRetry:
				fails++
			case holdBusy:
				busy++
			}
			mu.Unlock()
			if r == holdDone {
				g.remove(m)
			}
		}(m)
	}
	wg.Wait()
}

// sessionHold is a switchSession as a heldPlayer, for one hold.
type sessionHold struct {
	s *switchSession
	h *holdState
}

func (x sessionHold) stillHeld() bool {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	return x.s.hold == x.h && x.s.ctx.Err() == nil
}

func (x sessionHold) remind()              { x.s.sendClient(holdTitle(x.h.message)...) }
func (x sessionHold) reattach() holdResult { return x.s.reattach(x.h) }
func (x sessionHold) expire()              { x.s.holdExpired(x.h) }

// holdTitle shows message as a title that stays until the next one replaces it.
func holdTitle(message string) []packet.Packet {
	return []packet.Packet{
		&packet.SetTitle{ActionType: packet.TitleActionSetDurations, FadeInDuration: 0, RemainDuration: 70, FadeOutDuration: 10},
		&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: text.Colourf("<yellow>%s</yellow>", message)},
	}
}

// holdTitleClear takes the hold title away and resets the title timings to the game's.
func holdTitleClear() []packet.Packet {
	return []packet.Packet{
		&packet.SetTitle{ActionType: packet.TitleActionClear},
		&packet.SetTitle{ActionType: packet.TitleActionReset},
	}
}

// startHold holds the player for l's server, which just ended their link for reason, if the
// server has a hold and the link ended the way a restart does. It reports whether it did; if not,
// the caller moves the player on as before.
func (s *switchSession) startHold(l *link, reason string) bool {
	if s.reply != nil {
		return false // the console's stand-in
	}
	cfg := s.n.server(l.server.Name)
	if cfg == nil || cfg.Hold <= 0 || !holdable(l.err, cfg.HoldReasons) {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	if s.cur != l || s.transfer != nil || s.hold != nil || s.ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	deadline := holdDeadline(s.lastHold, l.server.Name, now, cfg.Hold)
	if !deadline.After(now.Add(holdInterval)) {
		s.mu.Unlock()
		s.log.Info("network: the server went down again right after coming back, not holding the player again", "server", l.server.Name)
		return false
	}
	msg := cfg.HoldMessage
	if msg == "" {
		msg = defaultHoldMessage
	}
	h := &holdState{server: l.server.Name, since: now, deadline: deadline, reason: reason, message: msg}
	s.hold = h
	s.settling.Store(false)
	s.placeholders = nil
	undo := s.clearClientLocked()
	s.mu.Unlock()

	s.sendClient(undo...)
	s.sendClient(&packet.ClientBoundCloseForm{}, &packet.StopSound{StopAll: true})
	s.sendClient(s.holdFreeze()...)
	s.sendClient(holdTitle(msg)...)
	s.log.Info("network: server went down, holding the player until it is back", "server", l.server.Name, "reason", reason, "for", deadline.Sub(now).Round(time.Second))
	n := s.n
	key := holdKey{n, strings.ToLower(l.server.Name)}
	joinHold(key, &holdMember{p: sessionHold{s, h}, deadline: deadline}, func() *holdGroup {
		return &holdGroup{probe: func() bool {
			c := n.server(key.server)
			return c != nil && serverUp(*c)
		}, interval: holdInterval, parallel: holdParallel, gap: holdGap}
	})
	return true
}

// holdFreeze keeps the player where they are while held: immobile, and no movement input.
func (s *switchSession) holdFreeze() []packet.Packet {
	return []packet.Packet{
		s.flags(map[uint32]bool{protocol.EntityDataFlagNoAI: true, protocol.EntityDataFlagBreathing: true, protocol.EntityDataFlagHasGravity: true}),
		&packet.UpdateClientInputLocks{Locks: inputLockMovement},
	}
}

// holding reports whether h is the player's current hold.
func (s *switchSession) holding(h *holdState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hold == h
}

// endHoldLocked ends the player's hold, if any, because something else (a move) takes over. s.mu
// held. It reports whether there was one: the caller takes the title away.
func (s *switchSession) endHoldLocked() bool {
	if s.hold == nil {
		return false
	}
	s.hold = nil
	return true
}

// holdExpired moves on a player whose server did not come back within the hold time (or came back
// in a state the client can't be put back into): as without a hold.
func (s *switchSession) holdExpired(h *holdState) {
	s.mu.Lock()
	if s.hold != h {
		s.mu.Unlock()
		return
	}
	s.hold = nil
	s.mu.Unlock()
	s.sendClient(holdTitleClear()...)
	s.log.Info("network: the held player's server is not back, moving them on", "server", h.server, "held", time.Since(h.since).Round(time.Second), "tries", h.tries.Load())
	s.moveAway(h.server, h.reason)
}

// reattach tries once to log a held player in to their server again and, if it answers, puts them
// back on it.
func (s *switchSession) reattach(h *holdState) holdResult {
	if !s.switchMu.TryLock() {
		return holdBusy // a move of the player's own: it ends the hold if it gets through
	}
	defer s.switchMu.Unlock()
	if !s.holding(h) || s.ctx.Err() != nil {
		return holdDone
	}
	server := s.n.server(h.server)
	if server == nil {
		go s.holdExpired(h) // removed from the servers by a reload
		return holdDone
	}
	h.tries.Add(1)
	start := time.Now()
	l, err := s.openLink(*server)
	if err == nil {
		select {
		case <-l.started:
		case <-l.closed:
			err = l.err
		case <-time.After(holdLoginTimeout):
			l.close()
			err = errors.New("no answer")
		case <-s.ctx.Done():
			l.close()
			return holdDone
		}
	}
	if err != nil {
		s.log.Debug("network: the held player's server is not back yet", "server", h.server, "err", err)
		return holdRetry
	}
	answered := time.Since(start)
	sg := l.startGame
	_, bm, perr := s.n.serverPalette(sg.Blocks, s.palette, sg.UseBlockNetworkIDHashes)
	s.mu.Lock()
	if s.hold != h {
		s.mu.Unlock()
		l.close()
		return holdDone
	}
	bad := ""
	if perr != nil {
		bad = perr.Error()
	} else if old := s.cur; old != nil && old.startGame != nil {
		bad = incompatible(old.startGame, sg)
	}
	if bad != "" {
		s.mu.Unlock()
		l.close()
		s.log.Warn("network: the server came back with settings this player's client can't take, moving them on", "server", h.server, "differences", bad)
		go s.holdExpired(h)
		return holdDone
	}
	if sg.Dimension != s.dimension {
		// Back in another dimension than the client is in (the player's saved position is in the
		// nether, say): only a dimension change gets them there, as at a move.
		s.mu.Unlock()
		s.log.Info("network: the held player's server is back with them in another dimension, moving them there", "server", h.server, "dimension", sg.Dimension)
		if !s.moveWithLink(l, *server, start, answered) {
			go s.holdExpired(h)
		}
		return holdDone
	}
	sg.Blocks = nil // the palette has them; the next move only compares settings
	s.hold = nil
	s.lastHold = endedHold{server: h.server, deadline: h.deadline, ended: time.Now()}
	l.rw = newRewriter(sg.EntityRuntimeID, sg.EntityUniqueID, s.clientRID, s.clientUID, bm)
	s.setItems(l)
	t := &transfer{start: start, answered: answered, held: time.Since(h.since), reattach: true, l: l,
		target: sg.Dimension, spawn: sg.PlayerPosition, rot: mgl32.Vec2{sg.Pitch, sg.Yaw}, wired: true}
	s.transfer = t
	s.cur = l
	requestMode := s.n.usesRequestMode(server.Name)
	if requestMode {
		s.settleLocked(sg.PlayerPosition) // see settle.go
		t.ground = s.awaitSpawnColumnLocked(sg.PlayerPosition)
	} else {
		s.settling.Store(false)
	}
	s.forgetColumns()
	s.placeholders = nil
	s.subChunkMode = requestMode
	injects := s.resetClientLocked(sg)
	s.mu.Unlock()

	s.log.Info("network: the held player's server is back, putting them on it again", "server", h.server, "held", t.held.Round(time.Millisecond), "tries", h.tries.Load())
	// All of this reaches the client before the first packet of the server's: what resetClientLocked
	// clears and sets must not undo what the server sends.
	s.sendClient(holdTitleClear()...)
	s.sendClient(injects...)
	s.sendClient(s.holdFreeze()...) // the reset woke it (sleeping): frozen until releaseAtSpawn
	s.sendClient(&packet.StopSound{StopAll: true}, s.position(t.spawn, t.rot))
	t.timer = time.AfterFunc(transferTimeout, func() { t.fail(s, "timed out") })
	s.setBackendPing(l)
	go s.readBackend(l, false)
	_ = l.c.WritePacket(&packet.RequestChunkRadius{ChunkRadius: s.chunkRadius, MaxChunkRadius: s.maxRadius})
	t.releaseAtSpawn(s)
	return holdDone
}

// abandon drops a reattach whose server went away again before it was done, without disconnecting
// the player: backendGone then holds them again (with the time left) or moves them on.
func (t *transfer) abandon(s *switchSession) {
	t.mu.Lock()
	t.finalized = true
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Unlock()
	s.mu.Lock()
	if s.transfer == t {
		s.transfer = nil
	}
	s.mu.Unlock()
}
