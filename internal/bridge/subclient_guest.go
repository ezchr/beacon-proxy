package bridge

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Split-screen players (sub-clients). A console playing split-screen sends the packets of its extra
// players on the main player's connection, marked with their sub-client id (1-3) in the packet
// header: bits 10-11 of the header varuint (BDS NetworkSystem's receive reads the handling sub-client
// there, and NetworkSystem::send writes the sub-client a server packet is for there too). An extra
// player starts with a SubClientLogin and leaves with a Disconnect from its id.
//
// Beacon makes each one a player of its own: a guest switchSession that shares the host's client
// connection (its packets come from the host's reader, its packets to the client go out with its id in
// the header) and has its own login to the backend, so servers see an ordinary second player. Guests
// move, are held and are kicked like any player; they end with the host's connection.

const maxGuests = 3

// subClientHeader rewrites a raw packet's header to address sub-client sub (bits 10-11), keeping the
// packet id and clearing the other sub-client bits.
func subClientHeader(raw []byte, sub byte) []byte {
	var v uint32
	n := 0
	for ; n < 5 && n < len(raw); n++ {
		v |= uint32(raw[n]&0x7f) << (7 * n)
		if raw[n]&0x80 == 0 {
			n++
			break
		}
	}
	if n == 0 {
		return raw
	}
	v = v&0x3FF | uint32(sub&3)<<10
	out := make([]byte, 0, len(raw)-n+3)
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	out = append(out, byte(v))
	return append(out, raw[n:]...)
}

// guestIdentity is the identity a split-screen player plays as. One with an Xbox account of its own
// keeps it; one without (a local profile, or the host's identity again) gets one derived from the
// host's XUID and its sub-client id, the same every time, and a name from the host's.
func guestIdentity(host login.IdentityData, sub byte, id login.IdentityData, authed bool) (login.IdentityData, bool) {
	if authed && id.XUID != "" && id.XUID != host.XUID && id.DisplayName != "" {
		return id, false
	}
	key := host.XUID + ":" + strconv.Itoa(int(sub))
	h := fnv.New64a()
	_, _ = h.Write([]byte("beacon-guest:" + key))
	out := login.IdentityData{
		// 19 digits starting 9: never a real XUID (those are 16 digits, 2535...).
		XUID:        "9" + fmt.Sprintf("%018d", h.Sum64()%1_000_000_000_000_000_000),
		Identity:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("beacon-guest:"+key)).String(),
		DisplayName: guestName(host.DisplayName, sub),
		TitleID:     id.TitleID,
	}
	return out, true
}

// guestName is "Host_N", at most 15 characters (a Java name through Geyser: 16 with Floodgate's prefix).
func guestName(host string, sub byte) string {
	base := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return -1
	}, host)
	if len(base) > 13 {
		base = base[:13]
	}
	return fmt.Sprintf("%s_%d", base, sub)
}

// routeSubClients takes the split-screen players' packets out of a batch the host's client sent and
// hands them to their guest sessions; the host's own are returned.
func (s *switchSession) routeSubClients(pks [][]byte) [][]byte {
	own := pks[:0]
	for _, raw := range pks {
		sub := subClientOf(raw)
		if sub == 0 {
			own = append(own, raw)
			continue
		}
		id, payload, err := splitPacket(raw)
		if err != nil {
			continue
		}
		if s.isPassthroughGuest(sub, id) {
			own = append(own, raw) // the server handles this guest (subclient_passthrough.go)
			continue
		}
		switch id {
		case packet.IDSubClientLogin:
			var pk packet.SubClientLogin
			if decodeInto(&pk, payload) == nil {
				go s.startGuest(sub, pk.ConnectionRequest)
			}
			continue
		case packet.IDDisconnect:
			if g := s.guest(sub); g != nil && g.cancel != nil {
				g.cancel(errors.New("left split-screen"))
			}
			continue
		}
		g := s.guest(sub)
		if g == nil || g.inbox == nil {
			continue // a split-screen player not logged in (yet): nothing of theirs may reach the host's server
		}
		select {
		case g.inbox <- g.guestPacket(subClientHeader(raw, 0)):
		default:
			g.log.Warn("network: split-screen player's packets are not being read, disconnecting them")
			g.disconnect("Too many packets.")
		}
	}
	return own
}

func (s *switchSession) guest(sub byte) *switchSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guests[sub]
}

// startGuest logs in the split-screen player sub of a host session, as handleNetwork does a player,
// and runs them until they leave or the host's connection ends.
func (s *switchSession) startGuest(sub byte, request []byte) {
	refuse := func(msg string) {
		_ = s.writeClient(subClientHeader(encodePacket(&packet.Disconnect{Message: msg}), sub))
	}
	if sub < 1 || sub > maxGuests || s.ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	ready, taken := s.cur != nil, s.guests[sub] != nil
	if ready && !taken {
		if s.guests == nil {
			s.guests = map[byte]*switchSession{}
		}
		s.guests[sub] = &switchSession{} // reserved while the login is checked
	}
	s.mu.Unlock()
	if !ready || taken {
		s.log.Info("network: split-screen login refused", "sub", sub, "hostJoined", ready, "subInUse", taken)
		if !taken {
			refuse("Wait until the first player has joined.")
		}
		return
	}
	release := func() {
		s.mu.Lock()
		delete(s.guests, sub)
		s.mu.Unlock()
	}
	g, cleanup, err := s.newGuest(sub, request)
	if err != nil {
		release()
		s.log.Info("network: split-screen login refused", "sub", sub, "reason", err)
		refuse(err.Error())
		return
	}
	s.mu.Lock()
	s.guests[sub] = g
	s.mu.Unlock()
	defer func() {
		release()
		cleanup()
	}()
	g.runGuest()
}

// newGuest checks a split-screen login and makes the guest's session. cleanup gives back what it
// took (its player slot, its place in the per-IP count).
func (s *switchSession) newGuest(sub byte, request []byte) (*switchSession, func(), error) {
	var undo []func()
	cleanup := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	fail := func(msg string) (*switchSession, func(), error) {
		cleanup()
		return nil, func() {}, errors.New(msg)
	}
	id, cd, auth, err := s.client.ParseLoginRequest(request)
	if err != nil {
		return fail("Could not read your login.")
	}
	gid, derived := guestIdentity(s.id, sub, id, auth.XBOXLiveAuthenticated)
	n := s.n
	ip := ""
	if _, _, addr, _ := dcVerdict(s.conn.RemoteAddr()); addr != nil {
		ip = addr.String()
	}
	if s.cfg.AllowXUID != nil && !s.cfg.AllowXUID(gid.XUID) && !(derived && s.cfg.AllowXUID(s.id.XUID)) {
		return fail("You are not allowed to join this server.")
	}
	if msg, closed := Closed(); closed && !n.IsAdmin(gid.XUID, gid.DisplayName) {
		return fail(msg)
	}
	if msg, banned := n.refuseLogin(gid.XUID, gid.DisplayName, ip); banned {
		return fail(msg)
	}
	if derived {
		if msg, banned := n.refuseLogin(s.id.XUID, s.id.DisplayName, ip); banned {
			return fail(msg)
		}
	}
	if ip != "" {
		rel, ok := holdSession(ip, gid.XUID)
		if !ok {
			return fail(tooManySessionsMessage)
		}
		undo = append(undo, rel)
	}
	if !takeSlot(n.IsAdmin(gid.XUID, gid.DisplayName)) {
		return fail(fullMessage)
	}
	undo = append(undo, func() { noteBedrock(-1) })
	if s.cfg.WorldPlayers != nil {
		s.cfg.WorldPlayers.Add(1)
		undo = append(undo, func() { s.cfg.WorldPlayers.Add(-1) })
	}
	log := s.cfg.Log
	if log == nil {
		log = s.log
	}
	log = log.With("player", gid.DisplayName, "host", s.id.DisplayName, "sub", sub)
	unchecked := true
	if Checks != nil {
		var kick string
		unchecked, kick = Checks.Login(gid, cd, s.conn.RemoteAddr(), log)
		if kick != "" {
			return fail(kick)
		}
	}
	cd.ThirdPartyName = gid.DisplayName
	if cd.PlayFabID == "" {
		cd.PlayFabID = strings.ToLower(id.PlayFabID)
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	g := &switchSession{
		packGen: s.packGen,
		n:       n, cfg: s.cfg, log: log, client: s.client, conn: s.conn, id: gid, cd: cd, proto: s.proto,
		pool: s.pool, ctx: ctx, cancel: cancel,
		chunkRadius: 16, maxRadius: 16, unchecked: unchecked,
		entities: map[int64]bool{}, players: map[uuid.UUID]bool{}, bossBars: map[int64]bool{}, waypoints: map[uuid.UUID]bool{},
		objectives: map[string]bool{}, scores: map[int64]protocol.ScoreboardEntry{}, links: map[int64]int64{},
		containers: map[byte]byte{}, hiddenHud: map[int32]bool{},
		host: s, sub: sub, derivedID: derived, inbox: make(chan []byte, 4096),
		guestLogin: encodePacket(&packet.Login{ClientProtocol: s.proto, ConnectionRequest: request}),
	}
	g.scrub = !g.isAdmin()
	undo = append(undo, func() { cancel(nil) })
	log.Info("network: split-screen player logging in", "xuid", gid.XUID, "ownAccount", !derived)
	return g, cleanup, nil
}

// runGuest joins a guest session and pumps it until it ends.
func (g *switchSession) runGuest() {
	n := g.n
	if old, ok := n.sessions.Swap(g.id.XUID, g); ok && old.(*switchSession) != g {
		old.(*switchSession).disconnect("You joined again from somewhere else.")
	}
	defer n.sessions.CompareAndDelete(g.id.XUID, g)
	if lr, ok := g.conn.(latencyReporter); ok {
		entry := registerPing(g.id.XUID, lr)
		defer unregisterPing(g.id.XUID, entry)
	}
	if err := g.join(); err != nil {
		g.log.Warn("network: split-screen join failed", "err", err)
		g.disconnect("Could not join: " + err.Error())
		return
	}
	if g.cfg.OnJoin != nil {
		g.cfg.OnJoin(g.id.XUID, g.id.DisplayName)
	}
	go g.readClient()
	<-g.ctx.Done()
	if cause := context.Cause(g.ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		g.log.Info("split-screen player left", "reason", cause)
	}
	g.mu.Lock()
	cur := g.cur
	g.mu.Unlock()
	if cur != nil {
		cur.close()
	}
}

// readPackets is the next batch from the client: the host reads the connection, a guest gets its
// share from the host's reader.
func (s *switchSession) readPackets() ([][]byte, error) {
	if s.host == nil {
		return s.client.ReadPackets()
	}
	select {
	case raw := <-s.inbox:
		pks := [][]byte{raw}
		for len(pks) < 64 {
			select {
			case raw := <-s.inbox:
				pks = append(pks, raw)
				continue
			default:
			}
			break
		}
		return pks, nil
	case <-s.ctx.Done():
		return nil, context.Cause(s.ctx)
	}
}

// loginPacket is the player's own Login packet (a guest's is made from its SubClientLogin).
func (s *switchSession) loginPacket() []byte {
	if s.host != nil {
		return s.guestLogin
	}
	return s.client.RawLoginPacket()
}

// followHost moves the host's split-screen players that were on from with the host to server.
func (s *switchSession) followHost(from string, server ServerConfig) {
	s.mu.Lock()
	var gs []*switchSession
	for _, g := range s.guests {
		if g != nil && g.host == s {
			gs = append(gs, g)
		}
	}
	s.mu.Unlock()
	for _, g := range gs {
		if cur := g.serverName(); cur != "" && !strings.EqualFold(cur, from) {
			continue // moved elsewhere on their own
		}
		go func(g *switchSession) {
			time.Sleep(500 * time.Millisecond)
			if err := g.requestSwitch(server, ""); err != nil {
				g.log.Info("network: split-screen player did not follow the host", "server", server.Name, "err", err)
			}
		}(g)
	}
}

// guestPacket puts the guest's own identity in the packets that carry the player's: the console fills
// in what it knows (the host's XUID, an empty one, or a zero UUID for a profile without an account),
// and servers refuse a chat line or skin that isn't the player's own (Dragonfly: "XUID must be equal
// to player's XUID", and the same for the name; Geyser kicked a host over a guest's zero-UUID PlayerSkin).
func (g *switchSession) guestPacket(raw []byte) []byte {
	id, payload, err := splitPacket(raw)
	if err != nil {
		return raw
	}
	switch id {
	case packet.IDText:
		var pk packet.Text
		if decodeInto(&pk, payload) == nil && (pk.XUID != g.id.XUID || pk.TextType == packet.TextTypeChat && pk.SourceName != g.id.DisplayName) {
			pk.XUID = g.id.XUID
			if pk.TextType == packet.TextTypeChat {
				pk.SourceName = g.id.DisplayName // Dragonfly: "SourceName must be equal to DisplayName"
			}
			return encodePacket(&pk)
		}
	case packet.IDPlayerSkin:
		var pk packet.PlayerSkin
		if u, err := uuid.Parse(g.id.Identity); err == nil && decodeInto(&pk, payload) == nil && pk.UUID != u {
			pk.UUID = u
			return encodePacket(&pk)
		}
	}
	return raw
}
