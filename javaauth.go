package main

import (
	"bufio"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robinbraemer/event"
	"go.minekube.com/common/minecraft/color"
	"go.minekube.com/common/minecraft/component"
	"go.minekube.com/gate/pkg/edition/java/profile"
	"go.minekube.com/gate/pkg/edition/java/proxy"
	"go.minekube.com/gate/pkg/util/uuid"
)

// Java logins for the whole network, ported from our Velocity plugin (HybridAuth's JavaAuth). The
// proxy runs in offline mode so cracked players can join.
//
// Premium names: every name is looked up with Mojang (cached on disk). A name that belongs to a paid
// Java account must pass Mojang's session check, so only its owner can use it. Those players keep the
// offline UUID they have always had here, except the names in premium-usernames.txt, which keep their
// real Mojang UUID. Backends are told with a zid_auth=premium profile property (the forwarding data is
// signed with the forwarding secret, players can't set it), so the login server doesn't ask for a
// password.
//
// Cracked players: one /register and /login for the network, kept by the login server, which answers
// on javaAuthOptions.URL. Each cracked connection gets a random session token, forwarded as
// zid_session; a login there counts for that token. Players start on the login server and can't go to
// the protected server until the login server says that connection has logged in. When it doesn't
// answer, the protected server is expected to ask them to log in itself.
//
// All of this is off unless [advanced] java-auth is set in beacon.toml.

// javaAuthOptions is [advanced] java-auth.
type javaAuthOptions struct {
	LoginServer     string `toml:"login-server"`
	ProtectedServer string `toml:"protected-server"`
	URL             string `toml:"url"`
}

const (
	premiumYes = 7 * 24 * time.Hour
	premiumNo  = 24 * time.Hour
)

type premiumEntry struct {
	Premium bool  `json:"premium"`
	Checked int64 `json:"checked"` // unix milliseconds, as the Velocity plugin wrote it
}

type javaAuth struct {
	javaAuthOptions
	log       *slog.Logger
	cacheFile string
	listed    map[string]bool // lower-case names that keep their real Mojang UUID
	http      *http.Client

	mu     sync.Mutex
	cache  map[string]premiumEntry
	tokens map[uuid.UUID]string // cracked players' session tokens
	proxy  *proxy.Proxy
}

func newJavaAuth(dir string, opts javaAuthOptions, log *slog.Logger) *javaAuth {
	a := &javaAuth{
		javaAuthOptions: opts,
		log:             log,
		cacheFile:       filepath.Join(dir, "premium_cache.json"),
		listed:          map[string]bool{},
		http:            &http.Client{Timeout: 3 * time.Second},
		cache:           map[string]premiumEntry{},
		tokens:          map[uuid.UUID]string{},
	}
	if f, err := os.Open(filepath.Join(dir, "premium-usernames.txt")); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			if name := strings.TrimSpace(s.Text()); name != "" && !strings.HasPrefix(name, "#") {
				a.listed[strings.ToLower(name)] = true
			}
		}
		f.Close()
	} else {
		log.Warn("no premium-usernames.txt: no player keeps a real Mojang UUID", "dir", dir)
	}
	if b, err := os.ReadFile(a.cacheFile); err == nil {
		if err := json.Unmarshal(b, &a.cache); err != nil {
			log.Warn("could not read premium cache", "file", a.cacheFile, "err", err)
		}
	}
	log.Info("java logins ready", "listedPremium", len(a.listed), "cachedNames", len(a.cache))
	return a
}

func (a *javaAuth) register(p *proxy.Proxy) error {
	a.proxy = p
	// Late priorities (lower runs later), so other subscribers have decided first.
	event.Subscribe(p.Event(), -100, a.onPreLogin)
	event.Subscribe(p.Event(), -100, a.onProfile)
	event.Subscribe(p.Event(), 100, a.onServerPreConnect)
	event.Subscribe(p.Event(), 0, a.onDisconnect)
	return nil
}

// premium reports whether name belongs to a paid Java account, from the cache or Mojang's API.
func (a *javaAuth) premium(name string) bool {
	key := strings.ToLower(name)
	a.mu.Lock()
	e, have := a.cache[key]
	a.mu.Unlock()
	now := time.Now()
	ttl := premiumNo
	if e.Premium {
		ttl = premiumYes
	}
	if have && now.Sub(time.UnixMilli(e.Checked)) < ttl {
		return e.Premium
	}
	resp, err := a.http.Get("https://api.mojang.com/users/profiles/minecraft/" + url.PathEscape(name))
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != 200 && resp.StatusCode != 204 && resp.StatusCode != 404 {
			err = fmt.Errorf("mojang API answered %d", resp.StatusCode)
		}
	}
	if err != nil {
		// Mojang unreachable: an earlier answer, however old, beats none; never looked up: cracked
		// (the password flow still protects the name on PvP).
		a.log.Warn("premium check failed", "name", name, "err", err)
		return have && e.Premium
	}
	fresh := premiumEntry{Premium: resp.StatusCode == 200, Checked: now.UnixMilli()}
	a.mu.Lock()
	a.cache[key] = fresh
	b, _ := json.Marshal(a.cache)
	a.mu.Unlock()
	if err := os.WriteFile(a.cacheFile, b, 0o644); err != nil {
		a.log.Warn("could not write premium cache", "err", err)
	}
	return fresh.Premium
}

func (a *javaAuth) onPreLogin(e *proxy.PreLoginEvent) {
	if e.Result() != proxy.AllowedPreLogin {
		return // denied, or already forced either way
	}
	name := e.Username()
	if a.listed[strings.ToLower(name)] || a.premium(name) {
		e.ForceOnlineMode()
		a.log.Info("premium name: Mojang login required", "name", name)
	}
}

func (a *javaAuth) onProfile(e *proxy.GameProfileRequestEvent) {
	p := e.GameProfile()
	if p.ID[0] == 0 && p.ID[1] == 0 && p.ID[2] == 0 && p.ID[3] == 0 && p.ID[4] == 0 && p.ID[5] == 0 && p.ID[6] == 0 && p.ID[7] == 0 {
		return // Floodgate (Bedrock): Xbox Live checked them
	}
	props := append([]profile.Property(nil), p.Properties...)
	if e.OnlineMode() {
		props = append(props, profile.Property{Name: "zid_auth", Value: "premium"})
		id := p.ID
		if !a.listed[strings.ToLower(p.Name)] {
			id = offlineUUID(p.Name)
		}
		e.SetGameProfile(profile.GameProfile{ID: id, Name: p.Name, Properties: props})
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	props = append(props, profile.Property{Name: "zid_session", Value: token})
	e.SetGameProfile(profile.GameProfile{ID: p.ID, Name: p.Name, Properties: props})
	a.mu.Lock()
	a.tokens[p.ID] = token
	a.mu.Unlock()
}

func (a *javaAuth) onServerPreConnect(e *proxy.ServerPreConnectEvent) {
	player := e.Player()
	a.mu.Lock()
	token, cracked := a.tokens[player.ID()]
	a.mu.Unlock()
	target := e.Server()
	if !cracked || target == nil || !strings.EqualFold(target.ServerInfo().Name(), a.ProtectedServer) {
		return
	}
	in, ok := a.loggedIn(player.Username(), token)
	if !ok || in {
		return // the login server is down (the protected one asks them itself), or they logged in
	}
	if e.PreviousServer() == nil {
		if login := a.proxy.Server(a.LoginServer); login != nil {
			e.Allow(login) // log in there first
			return
		}
	}
	e.Deny()
	msg := &component.Text{Content: "Log in first: /login <password> (or /register <password> <password>)", S: component.Style{Color: color.Red}}
	if e.PreviousServer() == nil {
		player.Disconnect(&component.Text{Content: "Log in on " + a.LoginServer + " first, then come to " + a.ProtectedServer + ".", S: component.Style{Color: color.Red}})
		return
	}
	_ = player.SendMessage(msg)
}

func (a *javaAuth) onDisconnect(e *proxy.DisconnectEvent) {
	player := e.Player()
	a.mu.Lock()
	token, cracked := a.tokens[player.ID()]
	delete(a.tokens, player.ID())
	a.mu.Unlock()
	if !cracked {
		return
	}
	go func() {
		u := a.URL + "end?name=" + url.QueryEscape(player.Username()) + "&token=" + token
		if resp, err := a.http.Post(u, "text/plain", nil); err == nil {
			resp.Body.Close()
		}
	}()
}

// loggedIn reports whether the login server says this connection (token) has logged in; ok is false
// when it didn't answer.
func (a *javaAuth) loggedIn(name, token string) (in, ok bool) {
	resp, err := a.http.Get(a.URL + "status?name=" + url.QueryEscape(name) + "&token=" + token)
	if err != nil {
		a.log.Info("the login server did not answer whether the player logged in", "server", a.LoginServer, "name", name, "err", err)
		return false, false
	}
	defer resp.Body.Close()
	var body struct {
		LoggedIn bool `json:"logged_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, false
	}
	return body.LoggedIn, true
}

// offlineUUID is the UUID an offline-mode server gives name: a version 3 UUID of
// "OfflinePlayer:"+name, as Java's UUID.nameUUIDFromBytes makes it.
func offlineUUID(name string) uuid.UUID {
	sum := md5.Sum([]byte("OfflinePlayer:" + name))
	sum[6] = sum[6]&0x0f | 0x30
	sum[8] = sum[8]&0x3f | 0x80
	return uuid.UUID(sum)
}
