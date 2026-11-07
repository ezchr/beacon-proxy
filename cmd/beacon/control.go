package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ezchr/beacon-proxy/internal/bridge"
)

// The control port: one loopback-only HTTP server per relay process for everything that used to
// have a port of its own.
//
//	/ping?xuid=...            real player latency, for the BDS ping plugin (bridge.PingHandler)
//	/invites/...              the primary broadcast's invite controls (invitewatcher.go,
//	/invites/friends/...        friendsinviter.go, friendadder.go)
//	/friends/add/...
//	/broadcast/<name>/...     the same invite controls for extra broadcast <name>, if it has
//	                          "invites": true
//	/debug/pprof/...          the Go profiler, only when running with -debug
//
// It is bound to 127.0.0.1 and has no authentication, as each of the three separate servers had:
// only something already on this machine can reach it.
//
// The server lives as long as the process. Invite controls belong to one Xbox Live session,
// though - every invite has to point at the session that is live now - so each session puts its
// handlers in on start (setInvites) and they are taken out when it ends. Between the two, the
// invite paths answer 503 rather than reaching a controller for a session that no longer exists:
// that is the same guarantee the old per-session server gave by shutting down with its session
// (see startInviteControlServer's history in invitewatcher.go, the 2026-09-22 orphaned loop).

// controlBroadcastPrefix is where extra broadcasts' invite controls live.
const controlBroadcastPrefix = "/broadcast/"

// primaryBroadcast is the name the primary broadcast's invite controls are kept under.
const primaryBroadcast = "primary"

// controlSrv is this process's control server, or nil in a process that runs none (a -login
// broadcaster, which shares config.json - and so its control_port - with the main relay).
var controlSrv *controlServer

type controlServer struct {
	debug bool

	mu      sync.RWMutex
	invites map[string]*inviteSlot // broadcast name -> its current session's invite routes
}

// inviteSlot is one session's invite routes. Held by pointer so a session ending can tell
// whether the slot is still its own - handlers themselves are not always comparable.
type inviteSlot struct{ h http.Handler }

// newControlServer returns a control server that is not listening yet - see listen.
func newControlServer(debug bool) *controlServer {
	return &controlServer{debug: debug, invites: map[string]*inviteSlot{}}
}

// listen serves c on addr in the background until ctx ends.
func (c *controlServer) listen(ctx context.Context, addr string, log *slog.Logger) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: c, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Warn("control server stopped", "err", err)
		}
	}()
	return nil
}

// setInvites puts h in as broadcast name's invite routes until sessionCtx ends.
func (c *controlServer) setInvites(sessionCtx context.Context, name string, h http.Handler) {
	slot := &inviteSlot{h: h}
	c.mu.Lock()
	c.invites[name] = slot
	c.mu.Unlock()
	go func() {
		<-sessionCtx.Done()
		c.mu.Lock()
		defer c.mu.Unlock()
		// Only if nothing newer has taken the slot: the next session may have put its own in
		// before this one's context was seen to end.
		if c.invites[name] == slot {
			delete(c.invites, name)
		}
	}()
}

func (c *controlServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if serveNetworkControl(w, r) {
		return
	}
	if serveChat(w, r) {
		return
	}
	switch {
	case path == "/moderation":
		if network == nil {
			http.Error(w, "needs relay mode", http.StatusNotFound)
			return
		}
		// by: the name of who runs it, when the Java proxy passes on an admin's command.
		_, _ = io.WriteString(w, strings.Join(network.ModerateAs(r.URL.Query().Get("cmd"), r.URL.Query().Get("by")), "\n")+"\n")
		return
	case path == "/moderation/java/login":
		if network == nil {
			http.Error(w, "needs relay mode", http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		msg, banned := network.JavaLogin(q.Get("uuid"), q.Get("name"), q.Get("ip"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"banned": banned, "message": msg})
		return
	case path == "/moderation/java/state":
		if network == nil {
			http.Error(w, "needs relay mode", http.StatusNotFound)
			return
		}
		var players []struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
			IP   string `json:"ip"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&players); err != nil {
			http.Error(w, "send a JSON list of {uuid, name, ip}", http.StatusBadRequest)
			return
		}
		list := make([]bridge.JavaPlayer, 0, len(players))
		for _, p := range players {
			list = append(list, bridge.JavaPlayer{UUID: p.UUID, Name: p.Name, IP: p.IP})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(network.JavaState(list))
		return
	case path == "/packetdebug":
		_, _ = io.WriteString(w, strings.Join(bridge.DebugCommand(r.URL.Query().Get("cmd")), "\n")+"\n")
		return
	case path == "/ping":
		bridge.PingHandler().ServeHTTP(w, r)
		return
	case strings.HasPrefix(path, "/debug/pprof"):
		if !c.debug {
			http.Error(w, "the profiler is only served when the relay runs with -debug", http.StatusNotFound)
			return
		}
		// net/http/pprof registers its handlers on http.DefaultServeMux.
		http.DefaultServeMux.ServeHTTP(w, r)
		return
	}

	name := primaryBroadcast
	if rest, ok := strings.CutPrefix(path, controlBroadcastPrefix); ok {
		var sub string
		name, sub, _ = strings.Cut(rest, "/")
		path = "/" + sub
	}
	if !strings.HasPrefix(path, "/invites/") && !strings.HasPrefix(path, "/friends/") {
		http.NotFound(w, r)
		return
	}
	c.mu.RLock()
	slot := c.invites[name]
	c.mu.RUnlock()
	if slot == nil {
		http.Error(w, "invite controls for broadcast "+name+" are not up: its Xbox Live session is starting, "+
			"or invites are off for it", http.StatusServiceUnavailable)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = path
	slot.h.ServeHTTP(w, r2)
}
