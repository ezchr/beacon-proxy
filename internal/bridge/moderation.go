package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Moderation for the relay's admins, across every server of the network:
//
//	/ban <player> [time] [reason]     /unban <player>     /banlist
//	/banip <player|ip> [time] [reason]   /unbanip <player|ip>
//	/mute <player> [time] [reason]    /unmute <player>    /mutelist
//
// time is such as 30m, 12h, 7d or 4w; left out, or perm, it has no end. The relay is the one place
// that sees a player's real XUID and address before any server does. A ban turns them away at
// login, and a mute stops their chat on its way to the server. They cover players relayed here, so
// not Java players on a proxy of their own. A player who is not an admin gets their server's command
// of the same name, if it has one.

// GamertagLookup finds the XUID of a gamertag on Xbox Live, for someone never seen here.
var GamertagLookup func(ctx context.Context, gamertag string) (string, error)

// modEntry is a ban or mute. Until is a Unix time; 0 has no end.
type modEntry struct {
	XUID   string `json:"xuid,omitempty"`
	Name   string `json:"name,omitempty"`
	IP     string `json:"ip,omitempty"`
	Reason string `json:"reason,omitempty"`
	By     string `json:"by,omitempty"`
	At     int64  `json:"at"`
	Until  int64  `json:"until,omitempty"`
}

func (e modEntry) over(now time.Time) bool { return e.Until != 0 && now.Unix() >= e.Until }

// left says how long is left of e.
func (e modEntry) left(now time.Time) string {
	if e.Until == 0 {
		return "permanent"
	}
	return time.Unix(e.Until, 0).Sub(now).Round(time.Minute).String()
}

// seenPlayer is who last joined under a name, so that someone offline can be banned.
type seenPlayer struct {
	XUID string `json:"xuid"`
	Name string `json:"name"`
	IP   string `json:"ip,omitempty"`
}

// modStore is the saved bans and mutes (moderation.json).
type modStore struct {
	mu     sync.Mutex
	path   string
	Bans   map[string]modEntry   `json:"bans"`    // by XUID
	IPBans map[string]modEntry   `json:"ip_bans"` // by address
	Mutes  map[string]modEntry   `json:"mutes"`   // by XUID
	Seen   map[string]seenPlayer `json:"seen"`    // by lower-case name
}

func newModStore(path string) *modStore {
	m := &modStore{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, m)
	}
	if m.Bans == nil {
		m.Bans = map[string]modEntry{}
	}
	if m.IPBans == nil {
		m.IPBans = map[string]modEntry{}
	}
	if m.Mutes == nil {
		m.Mutes = map[string]modEntry{}
	}
	if m.Seen == nil {
		m.Seen = map[string]seenPlayer{}
	}
	return m
}

// saveLocked writes the store, through a temporary file so that a crash cannot lose it.
func (m *modStore) saveLocked() error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// active returns the entry at key, dropping it if it is over.
func (m *modStore) active(list map[string]modEntry, key string, now time.Time) (modEntry, bool) {
	e, ok := list[key]
	if !ok {
		return modEntry{}, false
	}
	if e.over(now) {
		delete(list, key)
		_ = m.saveLocked()
		return modEntry{}, false
	}
	return e, true
}

// see remembers a player who joined.
func (m *modStore) see(xuid, name, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.ToLower(name)
	if p, ok := m.Seen[key]; ok && p.XUID == xuid && p.IP == ip && p.Name == name {
		return
	}
	m.Seen[key] = seenPlayer{XUID: xuid, Name: name, IP: ip}
	_ = m.saveLocked()
}

// refused returns the ban on a player or address, if there is one.
func (m *modStore) refused(xuid, ip string, now time.Time) (modEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.active(m.Bans, xuid, now); ok {
		return e, true
	}
	if ip != "" {
		return m.active(m.IPBans, ip, now)
	}
	return modEntry{}, false
}

// muted returns the mute on a player, if there is one.
func (m *modStore) muted(xuid string, now time.Time) (modEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active(m.Mutes, xuid, now)
}

// put adds an entry to a list.
func (m *modStore) put(list map[string]modEntry, key string, e modEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list[key] = e
	return m.saveLocked()
}

// remove takes an entry off a list, reporting whether it was there.
func (m *modStore) remove(list map[string]modEntry, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := list[key]; !ok {
		return false, nil
	}
	delete(list, key)
	return true, m.saveLocked()
}

// names lists a list's keys or names, dropping what is over.
func (m *modStore) entries(list map[string]modEntry, now time.Time) map[string]modEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]modEntry{}
	for k := range list {
		if e, ok := m.active(list, k, now); ok {
			out[k] = e
		}
	}
	return out
}

var (
	modStores   sync.Map // by path
	modTimeForm = regexp.MustCompile(`^(\d{1,6})([mhdw])$`)
)

// moderation is the network's store.
func (n *Network) moderation() *modStore {
	path := filepath.Join(n.Dir, "moderation.json")
	if m, ok := modStores.Load(path); ok {
		return m.(*modStore)
	}
	m, _ := modStores.LoadOrStore(path, newModStore(path))
	return m.(*modStore)
}

// parseModTime reads a time such as 30m, 7d or perm. 0 has no end.
func parseModTime(s string) (time.Duration, bool) {
	s = strings.ToLower(s)
	if s == "perm" || s == "permanent" {
		return 0, true
	}
	m := modTimeForm.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, _ := strconv.Atoi(m[1])
	if v == 0 {
		return 0, false
	}
	unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
	return time.Duration(v) * unit, true
}

// splitCommand splits a command line into words, keeping quoted ones together: gamertags can have
// spaces, and the client quotes them.
func splitCommand(line string) []string {
	var out []string
	var cur strings.Builder
	quoted, has := false, false
	for _, r := range strings.TrimPrefix(strings.TrimSpace(line), "/") {
		switch {
		case r == '"':
			quoted, has = !quoted, true
		case r == ' ' && !quoted:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

// modArgs reads the [time] [reason] after a target.
func modArgs(args []string) (time.Duration, string) {
	if len(args) == 0 {
		return 0, ""
	}
	if d, ok := parseModTime(args[0]); ok {
		return d, strings.Join(args[1:], " ")
	}
	return 0, strings.Join(args, " ")
}

// modTarget is a player a command names.
type modTarget struct {
	xuid, name, ip string
	online         *switchSession
}

// findTarget finds a player by name: online, seen here before, or on Xbox Live.
func (n *Network) findTarget(name string) (modTarget, bool) {
	if p, ok := n.FindPlayer(name); ok {
		t := modTarget{xuid: p.XUID, name: p.Name}
		if v, ok := n.sessions.Load(p.XUID); ok {
			t.online = v.(*switchSession)
			t.ip = t.online.remoteIP()
		}
		return t, true
	}
	m := n.moderation()
	m.mu.Lock()
	p, seen := m.Seen[strings.ToLower(name)]
	m.mu.Unlock()
	if seen {
		return modTarget{xuid: p.XUID, name: p.Name, ip: p.IP}, true
	}
	if GamertagLookup != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if xuid, err := GamertagLookup(ctx, name); err == nil && xuid != "" {
			return modTarget{xuid: xuid, name: name}, true
		}
	}
	return modTarget{}, false
}

// remoteIP is the player's address.
func (s *switchSession) remoteIP() string {
	if _, _, ip, _ := dcVerdict(s.conn.RemoteAddr()); ip != nil {
		return ip.String()
	}
	return ""
}

// banMessage is what a banned player reads.
func banMessage(e modEntry, now time.Time) string {
	msg := "§cYou are banned from this network."
	if e.Reason != "" {
		msg += "\n§fReason: " + e.Reason
	}
	if e.Until != 0 {
		msg += "\n§7Time left: " + e.left(now)
	}
	return msg
}

// refuseLogin notes a joining player and returns why they may not join, if so. Admins always may,
// so that a ban on a shared address cannot lock them out.
func (n *Network) refuseLogin(xuid, name, ip string) (string, bool) {
	m := n.moderation()
	m.see(xuid, name, ip)
	if n.IsAdmin(xuid, name) {
		return "", false
	}
	now := time.Now()
	e, ok := m.refused(xuid, ip, now)
	if !ok {
		return "", false
	}
	return banMessage(e, now), true
}

// mutedCommands talk to other players, so a muted player cannot use them.
var mutedCommands = map[string]bool{"msg": true, "tell": true, "w": true, "whisper": true, "me": true, "say": true, "r": true, "reply": true}

// mutedNow tells a muted player so and reports it.
func (s *switchSession) mutedNow() bool {
	now := time.Now()
	e, ok := s.n.moderation().muted(s.id.XUID, now)
	if !ok {
		return false
	}
	s.message("<red>You are muted (%s).</red>", e.left(now))
	return true
}

// mutedCommand reports whether a command line is one a muted player may not use.
func (s *switchSession) mutedCommand(line string) bool {
	f := splitCommand(line)
	return len(f) > 0 && mutedCommands[strings.ToLower(f[0])] && s.mutedNow()
}

// handleModeration runs a moderation command of an admin and reports whether it was one.
func (s *switchSession) handleModeration(line string) bool {
	f := splitCommand(line)
	if len(f) == 0 || !s.isAdmin() {
		return false
	}
	name, args := strings.ToLower(f[0]), f[1:]
	usage := map[string]string{
		"ban": "/ban <player> [time] [reason]", "unban": "/unban <player>",
		"banip": "/banip <player|ip> [time] [reason]", "unbanip": "/unbanip <player|ip>",
		"mute": "/mute <player> [time] [reason]", "unmute": "/unmute <player>",
	}
	switch name {
	case "banlist":
		s.listMod("Banned", s.n.moderation().Bans)
		s.listMod("Banned addresses", s.n.moderation().IPBans)
		return true
	case "mutelist":
		s.listMod("Muted", s.n.moderation().Mutes)
		return true
	}
	u, ok := usage[name]
	if !ok {
		return false
	}
	if len(args) == 0 {
		s.message("<red>Use %s. Time is such as 30m, 12h, 7d or 4w, or perm.</red>", u)
		return true
	}
	switch name {
	case "ban", "mute":
		s.punish(name, args[0], args[1:])
	case "banip":
		s.banIP(args[0], args[1:])
	case "unban", "unmute":
		s.pardon(name, args[0])
	case "unbanip":
		s.unbanIP(args[0])
	}
	return true
}

// entry makes a ban or mute by this admin.
func (s *switchSession) entry(t modTarget, ip string, args []string) modEntry {
	d, reason := modArgs(args)
	now := time.Now()
	e := modEntry{XUID: t.xuid, Name: t.name, IP: ip, Reason: reason, By: s.id.DisplayName, At: now.Unix()}
	if d > 0 {
		e.Until = now.Add(d).Unix()
	}
	return e
}

func (s *switchSession) punish(kind, who string, args []string) {
	t, ok := s.n.findTarget(who)
	if !ok {
		s.message("<red>No player named %s was found.</red>", who)
		return
	}
	if s.n.IsAdmin(t.xuid, t.name) {
		s.message("<red>%s is an admin.</red>", t.name)
		return
	}
	m := s.n.moderation()
	e := s.entry(t, "", args)
	list := m.Bans
	if kind == "mute" {
		list = m.Mutes
	}
	if err := m.put(list, t.xuid, e); err != nil {
		s.message("<red>Done, but it could not be saved: %s</red>", err)
	}
	now := time.Now()
	if kind == "ban" {
		if t.online != nil {
			t.online.disconnect(banMessage(e, now))
		}
		s.message("<green>Banned %s (%s).</green>", t.name, e.left(now))
		return
	}
	if t.online != nil {
		t.online.message("<red>You were muted (%s).</red>", e.left(now))
	}
	s.message("<green>Muted %s (%s).</green>", t.name, e.left(now))
}

func (s *switchSession) pardon(kind, who string) {
	m := s.n.moderation()
	list, verb := m.Bans, "banned"
	if kind == "unmute" {
		list, verb = m.Mutes, "muted"
	}
	t, ok := s.n.listedTarget(list, who)
	if !ok {
		s.message("<red>%s is not %s.</red>", who, verb)
		return
	}
	if _, err := m.remove(list, t); err != nil {
		s.message("<red>Done, but it could not be saved: %s</red>", err)
	}
	if v, ok := s.n.sessions.Load(t); ok && kind == "unmute" {
		v.(*switchSession).message("<green>You can talk again.</green>")
	}
	s.message("<green>%s is no longer %s.</green>", who, verb)
}

// listedTarget finds the key of an entry by the name on it, or the player of that name.
func (n *Network) listedTarget(list map[string]modEntry, who string) (string, bool) {
	m := n.moderation()
	m.mu.Lock()
	for k, e := range list {
		if strings.EqualFold(e.Name, who) {
			m.mu.Unlock()
			return k, true
		}
	}
	m.mu.Unlock()
	t, ok := n.findTarget(who)
	if !ok {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok = list[t.xuid]
	return t.xuid, ok
}

// modIP is the address a /banip names: itself, or a player's.
func (s *switchSession) modIP(who string) (string, modTarget, bool) {
	if ip := net.ParseIP(who); ip != nil {
		return ip.String(), modTarget{}, true
	}
	t, ok := s.n.findTarget(who)
	if !ok || t.ip == "" {
		return "", t, false
	}
	return t.ip, t, true
}

func (s *switchSession) banIP(who string, args []string) {
	ip, t, ok := s.modIP(who)
	if !ok {
		s.message("<red>No address is known for %s. Give the address itself.</red>", who)
		return
	}
	if ip == s.remoteIP() {
		s.message("<red>That is your own address.</red>")
		return
	}
	if t.xuid != "" && s.n.IsAdmin(t.xuid, t.name) {
		s.message("<red>%s is an admin.</red>", t.name)
		return
	}
	m := s.n.moderation()
	e := s.entry(t, ip, args)
	if err := m.put(m.IPBans, ip, e); err != nil {
		s.message("<red>Done, but it could not be saved: %s</red>", err)
	}
	now := time.Now()
	dropped := 0
	s.n.sessions.Range(func(_, v any) bool {
		o := v.(*switchSession)
		if o.remoteIP() == ip && !s.n.IsAdmin(o.id.XUID, o.id.DisplayName) {
			o.disconnect(banMessage(e, now))
			dropped++
		}
		return true
	})
	s.message("<green>Banned the address of %s (%s), %d player(s) removed.</green>", who, e.left(now), dropped)
}

func (s *switchSession) unbanIP(who string) {
	m := s.n.moderation()
	key := ""
	if ip := net.ParseIP(who); ip != nil {
		key = ip.String()
	} else {
		m.mu.Lock()
		for k, e := range m.IPBans {
			if strings.EqualFold(e.Name, who) {
				key = k
			}
		}
		m.mu.Unlock()
		if key == "" {
			if ip, _, ok := s.modIP(who); ok {
				key = ip
			}
		}
	}
	if was, err := m.remove(m.IPBans, key); err != nil {
		s.message("<red>Done, but it could not be saved: %s</red>", err)
	} else if !was {
		s.message("<red>%s is not banned.</red>", who)
		return
	}
	s.message("<green>The address of %s is no longer banned.</green>", who)
}

// listMod shows a list to the admin, a line each.
func (s *switchSession) listMod(title string, list map[string]modEntry) {
	now := time.Now()
	entries := s.n.moderation().entries(list, now)
	s.message("<yellow>%s: %d</yellow>", title, len(entries))
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		return strings.Compare(strings.ToLower(entries[a].Name), strings.ToLower(entries[b].Name))
	})
	for i, k := range keys {
		if i == 25 {
			s.message("<grey>and %d more</grey>", len(keys)-i)
			break
		}
		e := entries[k]
		label := e.Name
		if e.IP != "" {
			label = strings.TrimSpace(e.IP + " " + e.Name)
		}
		s.message("<grey>%s</grey>", strings.TrimSpace(fmt.Sprintf("%s - %s, by %s %s", label, e.left(now), e.By, e.Reason)))
	}
}

// injectModeration adds the moderation commands to an admin's command list, in place of a server's
// own of the same name. enum adds an enum and returns its index.
func (s *switchSession) injectModeration(pk *packet.AvailableCommands, enum func(string, []string) uint32) {
	const valid = protocol.CommandArgValid
	set := func(name, description string, overloads []protocol.CommandOverload) {
		for i, c := range pk.Commands {
			if c.Name == name {
				pk.Commands[i].Description, pk.Commands[i].Overloads = description, overloads
				return
			}
		}
		pk.Commands = append(pk.Commands, protocol.Command{Name: name, Description: description, AliasesOffset: 0xffffffff, Overloads: overloads})
	}
	// target overloads: the names known, as suggestions, and any other name typed out.
	target := func(enumName, param string, names []string, rest ...protocol.CommandParameter) []protocol.CommandOverload {
		typed := protocol.CommandOverload{Parameters: append([]protocol.CommandParameter{{Name: param, Type: valid | protocol.CommandArgTypeString}}, rest...)}
		if len(names) == 0 {
			return []protocol.CommandOverload{typed}
		}
		first := protocol.CommandParameter{Name: param, Type: valid | protocol.CommandArgEnum | enum(enumName, names)}
		return []protocol.CommandOverload{{Parameters: append([]protocol.CommandParameter{first}, rest...)}, typed}
	}
	rest := []protocol.CommandParameter{
		{Name: "time", Type: valid | protocol.CommandArgTypeString, Optional: true},
		{Name: "reason", Type: valid | protocol.CommandArgTypeRawText, Optional: true},
	}
	var online []string
	for _, p := range s.n.Players() {
		online = append(online, p.Name)
	}
	m := s.n.moderation()
	now := time.Now()
	listed := func(list map[string]modEntry, keys bool) []string {
		var out []string
		for k, e := range m.entries(list, now) {
			if keys {
				out = append(out, k)
			} else if e.Name != "" {
				out = append(out, e.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	set("ban", "Ban a player from the whole network", target("BeaconBanPlayer", "player", online, rest...))
	set("mute", "Mute a player on the whole network", target("BeaconMutePlayer", "player", online, rest...))
	set("banip", "Ban a player's address from the whole network", target("BeaconBanIP", "player", online, rest...))
	set("unban", "Lift a ban", target("BeaconBanned", "player", listed(m.Bans, false)))
	set("unmute", "Lift a mute", target("BeaconMuted", "player", listed(m.Mutes, false)))
	set("unbanip", "Lift an address ban", target("BeaconBannedIP", "address", listed(m.IPBans, true)))
	set("banlist", "List the network's bans", []protocol.CommandOverload{{}})
	set("mutelist", "List the network's mutes", []protocol.CommandOverload{{}})
}
