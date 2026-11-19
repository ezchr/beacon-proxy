package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ezchr/beacon-proxy/internal/session"
	"github.com/ezchr/beacon-proxy/internal/xbl"
)

// A world's inviter ([[broadcast.extra]] inviter = "<other broadcast>"): the other broadcast's
// account joins this world's newest Friends-tab session as a member, without joining the world
// itself, and sends this world's invites from there. It follows rollover (rollover.go): when a
// new session opens it joins that one and leaves the one before, so emptied sessions still close.
// The account must be friends with the host (Joinability "joinable_by_friends"): the inviter sends
// the host a friend request, which the host accepts unless accept-friends is off for it.

// memberAccount is a broadcast's signed-in account, for other worlds' inviter.
type memberAccount struct {
	auth     *session.Session
	xuid     string
	gamertag string
	rta      *xbl.RTA
}

// memberAccounts holds every running broadcast's account, by broadcast name.
var memberAccounts sync.Map // name -> *memberAccount

// memberCheckInterval is how often the inviter checks it is still in the newest session.
const memberCheckInterval = 20 * time.Second

type sessionMember struct {
	name     string // the member's own broadcast
	activity bool   // also the account's activity: its friends see this world (Members)
	hostXUID string
	sessions *sessionSet
	log      *slog.Logger

	mu        sync.Mutex
	joined    *xbl.JoinedSession
	joinedID  string // session id joined
	joinedCID string // RTA connection id the membership was made with
	friended  bool
}

func newSessionMember(name, hostXUID string, sessions *sessionSet, log *slog.Logger) *sessionMember {
	return &sessionMember{name: name, hostXUID: hostXUID, sessions: sessions, log: log.With("member", name)}
}

// account waits for the inviting broadcast's account to be signed in.
func (m *sessionMember) account(ctx context.Context) (*memberAccount, error) {
	for {
		name := m.name
		if name == mainWorld {
			name = primaryBroadcast // the main world signs in as "primary"
		}
		if v, ok := memberAccounts.Load(name); ok {
			return v.(*memberAccount), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// run keeps the inviter in the newest session until ctx ends, then leaves.
func (m *sessionMember) run(ctx context.Context) {
	acc, err := m.account(ctx)
	if err != nil {
		return
	}
	if acc.xuid == m.hostXUID {
		m.log.Error("the inviter is the host's own account: nothing to join")
		return
	}
	m.log.Info("inviter account ready", "gamertag", acc.gamertag)
	t := time.NewTicker(memberCheckInterval)
	defer t.Stop()
	for {
		if err := m.ensure(ctx, acc); err != nil && ctx.Err() == nil {
			m.log.Warn("inviter could not join the world's session", "err", err)
		}
		select {
		case <-ctx.Done():
			m.leave()
			return
		case <-t.C:
		}
	}
}

// ensure makes the inviter a member of the newest session, with its live RTA connection.
func (m *sessionMember) ensure(ctx context.Context, acc *memberAccount) error {
	cur := m.sessions.current().SessionID()
	cid, err := acc.rta.ConnectionID(ctx)
	if err != nil {
		return fmt.Errorf("rta connection: %w", err)
	}
	m.mu.Lock()
	j, same := m.joined, m.joined != nil && m.joinedID == cur && m.joinedCID == cid
	oldID := m.joinedID
	m.mu.Unlock()
	if same {
		if in, err := j.HasMember(ctx, acc.xuid); err == nil && in {
			m.keepActivity(ctx, acc, j, cur)
			return nil
		}
	}
	ref := xbl.SessionRef{Scid: xbl.ServiceConfigID, TemplateName: xbl.TemplateName, Name: cur}
	nj, err := xbl.JoinSessionRef(ctx, acc.auth, acc.xuid, cid, ref)
	if err != nil {
		m.befriend(ctx, acc)
		return err
	}
	m.mu.Lock()
	m.joined, m.joinedID, m.joinedCID = nj, cur, cid
	m.mu.Unlock()
	m.keepActivity(ctx, acc, nj, cur)
	if oldID != cur {
		m.log.Info("inviter joined the world's session (not the world)", "session", cur)
		if j != nil {
			if err := j.Leave(ctx); err != nil {
				m.log.Warn("inviter could not leave the session before", "session", oldID, "err", err)
			}
		}
	}
	return nil
}

// befriend sends the host a friend request once, as joining a joinable_by_friends session needs.
func (m *sessionMember) befriend(ctx context.Context, acc *memberAccount) {
	m.mu.Lock()
	done := m.friended
	m.friended = true
	m.mu.Unlock()
	if done {
		return
	}
	res, err := xbl.SendFriendRequest(ctx, acc.auth, m.hostXUID)
	m.log.Info("inviter sent the host a friend request (joining the session needs friendship)", "status", res.Status, "err", err)
}

func (m *sessionMember) leave() {
	m.mu.Lock()
	j := m.joined
	m.joined = nil
	m.mu.Unlock()
	if j == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = j.Leave(ctx)
}

// SendInvite invites xuid into the newest session from the inviter's account.
func (m *sessionMember) SendInvite(ctx context.Context, xuid string) error {
	acc, err := m.account(ctx)
	if err != nil {
		return err
	}
	if err := m.ensure(ctx, acc); err != nil {
		return fmt.Errorf("inviter not in the session: %w", err)
	}
	return xbl.SendInviteAs(ctx, acc.auth, m.sessions.current().SessionID(), xuid, m.log)
}

// keepActivity makes the session j (id cur) the member account's activity, when it is one of the
// world's members, unless it already is: the account's friends then see the world on their Friends
// tab, through it. A member that only invites has none: being in a session alone shows nothing
// (an account that only invites, a member of the session, was checked to have no activity).
func (m *sessionMember) keepActivity(ctx context.Context, acc *memberAccount, j *xbl.JoinedSession, cur string) {
	if !m.activity || j == nil {
		return
	}
	if acts, err := xbl.ActivitiesForXUIDs(ctx, acc.auth, []string{acc.xuid}); err == nil {
		for _, a := range acts {
			if a.Ref.Name == cur {
				return
			}
		}
	}
	if err := j.SetActivity(ctx); err != nil {
		m.log.Warn("member could not make the world its activity", "session", cur, "err", err)
		return
	}
	m.log.Info("member shows the world to its friends (its activity)", "session", cur, "gamertag", acc.gamertag)
}

// inviterPool sends a world's invites from several member accounts, each in turn; the invite loop
// sends Parallel invites at a time, one from each account.
type inviterPool struct {
	members []*sessionMember
	next    atomic.Uint64
}

func (p *inviterPool) SendInvite(ctx context.Context, xuid string) error {
	m := p.members[(p.next.Add(1)-1)%uint64(len(p.members))]
	return m.SendInvite(ctx, xuid)
}

// Parallel is how many invites the pool sends at a time: one per account.
func (p *inviterPool) Parallel() int { return len(p.members) }

// startSessionMembers starts a sessionMember for every account the world's members, inviters and
// inviter name, and returns the pool of its inviters, or nil when it has none.
func startSessionMembers(ctx context.Context, cfg FileConfig, hostXUID string, sessions *sessionSet, log *slog.Logger) *inviterPool {
	inviters := append([]string(nil), cfg.Inviters...)
	if cfg.Inviter != "" && !slices.Contains(inviters, cfg.Inviter) {
		inviters = append(inviters, cfg.Inviter)
	}
	names := append([]string(nil), cfg.Members...)
	for _, n := range inviters {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	byName := map[string]*sessionMember{}
	for _, n := range names {
		m := newSessionMember(n, hostXUID, sessions, log)
		m.activity = slices.Contains(cfg.Members, n)
		byName[n] = m
		go m.run(ctx)
	}
	if len(inviters) == 0 {
		return nil
	}
	pool := &inviterPool{}
	for _, n := range inviters {
		pool.members = append(pool.members, byName[n])
	}
	return pool
}
