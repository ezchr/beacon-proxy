package bridge

import (
	"strings"
	"time"
)

// Java players never come through Beacon: a Java proxy in front of your servers (Velocity, with
// the HybridAuth plugin) asks Beacon at each login and every few seconds after, through the
// control API, so one list of bans and mutes covers both editions. Beacon keeps a Java player
// under java:<uuid> in its lists and under java:<name> among the players it has seen, apart from
// the Xbox gamertags: /ban Steve finds a Bedrock player first, /ban java:Steve only the Java one.

// javaKey is the key of a Java player's ban or mute.
func javaKey(uuid string) string { return "java:" + strings.ToLower(uuid) }

// JavaLogin notes a Java player logging in through the Java proxy and returns why they may not
// join, if so. A Java name proves nothing (offline-mode servers take any), so no Java player counts
// as one of Beacon's admins: an address ban stops them too.
func (n *Network) JavaLogin(uuid, name, ip string) (string, bool) {
	if uuid == "" || name == "" {
		return "", false
	}
	m := n.moderation()
	m.see(javaKey(uuid), "java:"+name, ip)
	if moderationOff.Load() {
		return "", false
	}
	now := time.Now()
	e, ok := m.refused(javaKey(uuid), ip, now)
	if !ok {
		return "", false
	}
	return banMessage(e, now), true
}

// JavaStatus is what the Java proxy enforces on a Java player who is on: Ban, the message to
// remove them with; Mute, how long they are still muted for. Empty when there is none.
type JavaStatus struct {
	Ban  string `json:"ban,omitempty"`
	Mute string `json:"mute,omitempty"`
}

// JavaPlayer is a Java player who is on, as the Java proxy reports them.
type JavaPlayer struct {
	UUID, Name, IP string
}

// JavaState returns the bans and mutes on the Java players who are on, by UUID; players with
// neither are left out.
func (n *Network) JavaState(players []JavaPlayer) map[string]JavaStatus {
	out := map[string]JavaStatus{}
	if moderationOff.Load() {
		return out
	}
	m := n.moderation()
	now := time.Now()
	for _, p := range players {
		var st JavaStatus
		if e, ok := m.refused(javaKey(p.UUID), p.IP, now); ok {
			st.Ban = banMessage(e, now)
		}
		if e, ok := m.muted(javaKey(p.UUID), now); ok {
			st.Mute = e.left(now)
		}
		if st != (JavaStatus{}) {
			out[strings.ToLower(p.UUID)] = st
		}
	}
	return out
}

// findJava finds a Java player seen through the Java proxy, by name.
func (n *Network) findJava(name string) (modTarget, bool) {
	m := n.moderation()
	m.mu.Lock()
	p, ok := m.Seen["java:"+strings.ToLower(name)]
	m.mu.Unlock()
	if !ok {
		return modTarget{}, false
	}
	return modTarget{xuid: p.XUID, name: strings.TrimPrefix(p.Name, "java:"), ip: p.IP}, true
}

// ModerateAs runs a moderation command for someone the Java proxy let use it: as the console
// does, but with their name on what they do.
func (n *Network) ModerateAs(line, by string) []string {
	if by == "" {
		return n.Moderate(line)
	}
	var out []string
	say := func(text string) { out = append(out, text) }
	if f := splitCommand(line); len(f) > 0 && strings.EqualFold(f[0], "admin") {
		return []string{"The admin command is for Beacon's console."}
	}
	if moderationOff.Load() {
		return []string{"Moderation is off: moderation = false in beacon.toml."}
	}
	// Unlike the console, they may not ban or mute one of Beacon's admins.
	if f := splitCommand(line); len(f) > 1 {
		switch strings.ToLower(f[0]) {
		case "ban", "banip", "mute":
			if t, ok := n.findTarget(f[1]); ok && !isJavaKey(t.xuid) && n.IsAdmin(t.xuid, t.name) {
				return []string{t.name + " is an admin."}
			}
		}
	}
	s := &switchSession{n: n, reply: say}
	s.id.DisplayName = by
	if !s.handleModeration(line) && len(out) == 0 {
		return modHelp
	}
	return out
}

// isJavaKey reports whether a list key is a Java player's.
func isJavaKey(key string) bool { return strings.HasPrefix(key, "java:") }
