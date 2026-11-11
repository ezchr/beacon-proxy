package bridge

import (
	"os"
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// SubClientPassthrough (env BEACON_SUBCLIENT_PASSTHROUGH, a comma list of server names, or "*" for
// all) names the servers that handle split-screen guests themselves. A guest that logs in while its
// host is on one of them is passed through: its packets keep their header sub-client ids and go over
// the host's backend connection, and server packets for it come back with its id and go to the client
// with it. A guest that logs in on any other server gets Beacon's own guest handling
// (subclient_guest.go), which makes it a player of its own.
//
// Guest packets go through the host link's rewriter like the host's (blocks, items, the host's
// entity ids) and keep their sub-client id. A guest's StartGame gets the union's blocks and settings,
// and its item and entity lists are the union's, as the host's are. A guest only plays on the server
// it logged in to: when the host moves to another server, the guest is disconnected with a message
// and can join again, getting the mode of the new server. Guests with Beacon's own handling are
// separate sessions and are not affected by the host's moves.
var SubClientPassthrough = parsePassthrough(os.Getenv("BEACON_SUBCLIENT_PASSTHROUGH"))

// parsePassthrough reads the server list of BEACON_SUBCLIENT_PASSTHROUGH.
func parsePassthrough(v string) map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Split(v, ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			out[name] = true
		}
	}
	return out
}

// passthroughServer reports whether guests on the server named pass through to it.
func passthroughServer(name string) bool {
	return SubClientPassthrough["*"] || SubClientPassthrough[strings.ToLower(name)]
}

// isPassthroughGuest reports whether a client packet of sub-client sub belongs to a passed-through
// guest: one already logged in that way, or a SubClientLogin while the host is on a passthrough
// server and the sub-client is not one of Beacon's own guests.
func (s *switchSession) isPassthroughGuest(sub byte, id uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ptGuests[sub]; ok {
		return true
	}
	if id != packet.IDSubClientLogin || s.cur == nil || !passthroughServer(s.cur.server.Name) {
		return false
	}
	_, beaconGuest := s.guests[sub]
	return !beaconGuest
}

// withSub gives out the sub-client bits (10-13) of orig when rewriting it lost them.
func withSub(orig, out []byte) []byte {
	bits := headerValue(orig) &^ 0x3FF
	if bits == 0 || len(out) == 0 || headerValue(out)&^0x3FF == bits {
		return out
	}
	return withHeaderBits(out, bits)
}

// passGuestToServer handles a guest's packet from the client in passthrough mode. It returns the
// packet to send to the server, or nil to send nothing.
func (s *switchSession) passGuestToServer(cur *link, raw []byte, id uint32) []byte {
	sub := clientSubClient(raw)
	if id == packet.IDSubClientLogin && cur != nil {
		s.mu.Lock()
		_, already := s.ptGuests[sub]
		s.mu.Unlock()
		if !already {
			gid, msg, ok := s.admitPassthroughGuest(sub, raw)
			if !ok {
				_ = ptWriteClient(s, subClientHeader(encodePacket(&packet.Disconnect{Message: msg}), sub))
				return nil
			}
			s.mu.Lock()
			if s.ptGuests == nil {
				s.ptGuests, s.ptIDs, s.ptLogins = map[byte]*link{}, map[byte]login.IdentityData{}, map[byte][]byte{}
			}
			s.ptGuests[sub], s.ptIDs[sub], s.ptLogins[sub] = cur, gid, append([]byte(nil), raw...)
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	on, known := s.ptGuests[sub]
	if id == packet.IDDisconnect {
		s.ptForgetLocked(sub)
	}
	s.mu.Unlock()
	if !known || cur == nil {
		return nil
	}
	if on != cur {
		// The host is moving: the guest follows it (followHost) and is not on this link yet.
		return nil
	}
	if id == packet.IDCommandRequest {
		if target, ok := s.guestServerCommand(raw); ok {
			s.guestMovesAlone(sub, target)
			return nil
		}
	}
	rw, err := cur.rw.rewrite(raw, false, s.pool)
	if err != nil {
		return nil
	}
	return withSub(raw, rw)
}

// passGuestToClient handles a server packet for a guest in passthrough mode. It returns the packet
// to send to the client, or nil to drop it.
func (s *switchSession) passGuestToClient(l *link, raw []byte, id uint32, payload []byte) []byte {
	sub := subClientOf(raw)
	u := s.ptUnion
	switch id {
	case packet.IDStartGame:
		if u == nil {
			break
		}
		var sg packet.StartGame
		if decodeInto(&sg, payload) != nil {
			return nil
		}
		// The guest's own entity IDs go through the host link's swap like every other entity ID: the
		// host's IDs differ between client and server after a move, and the console has one ID space.
		sg.EntityRuntimeID = l.rw.swapRID(sg.EntityRuntimeID)
		sg.EntityUniqueID = l.rw.swapUID(sg.EntityUniqueID)
		sg.Blocks = u.blocks
		sg.UseBlockNetworkIDHashes = false
		sg.Experiments = u.exps
		sg.ExperimentsPreviouslyToggled = len(u.exps) > 0
		sg.PlayerMovementSettings.RewindHistorySize = u.rewind
		if MultiPlayerOff {
			sg.MultiPlayerGame = false
		}
		return subClientHeader(encodePacket(&sg), sub)
	case packet.IDItemRegistry:
		if u == nil {
			break
		}
		s.mu.Lock()
		if s.ptItems == nil {
			s.ptItems = map[byte]bool{}
		}
		sent := s.ptItems[sub]
		s.ptItems[sub] = true
		s.mu.Unlock()
		if sent {
			return nil // as for the host: only the first item list is the registry
		}
		return subClientHeader(encodePacket(u.items), sub)
	case packet.IDAvailableActorIdentifiers:
		if u != nil {
			return subClientHeader(encodePacket(u.actors), sub)
		}
	case packet.IDDisconnect:
		s.mu.Lock()
		s.ptForgetLocked(sub)
		s.mu.Unlock()
	}
	rw, err := l.rw.rewrite(raw, true, s.pool)
	if err != nil {
		s.log.Debug("network: could not rewrite a guest packet, dropped", "id", id, "sub", sub, "err", err)
		return nil
	}
	return withSub(raw, rw)
}

// ptWriteClient writes a packet to the client; tests replace it.
var ptWriteClient = func(s *switchSession, raw []byte) error { return s.writeClient(raw) }

// withHeaderBits returns raw with its header's bits above the packet id (10-13) set to bits.
func withHeaderBits(raw []byte, bits uint32) []byte {
	v := headerValue(raw)
	n := 0
	for n < len(raw) && n < 5 {
		n++
		if raw[n-1]&0x80 == 0 {
			break
		}
	}
	v = v&0x3FF | bits
	out := make([]byte, 0, len(raw)-n+3)
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	out = append(out, byte(v))
	return append(out, raw[n:]...)
}

// admitPassthroughGuest checks a passed-through guest in as Beacon's own guests are: it reads its
// login for the identity it plays as (derived from the host's without an account of its own), refuses
// banned players and counts it towards the player limit, the online count and its Friends-tab world.
func (s *switchSession) admitPassthroughGuest(sub byte, raw []byte) (login.IdentityData, string, bool) {
	gid := login.IdentityData{}
	if _, payload, err := splitPacket(raw); err == nil {
		var pk packet.SubClientLogin
		if decodeInto(&pk, payload) == nil && s.client != nil {
			if id, _, auth, err := s.client.ParseLoginRequest(pk.ConnectionRequest); err == nil {
				gid, _ = guestIdentity(s.id, sub, id, auth.XBOXLiveAuthenticated)
			}
		}
	}
	if gid.DisplayName == "" {
		// Not readable here: the server checks the login itself; it is listed as the host's guest.
		gid, _ = guestIdentity(s.id, sub, login.IdentityData{}, false)
	}
	if s.n != nil {
		if msg, banned := s.n.refuseLogin(gid.XUID, gid.DisplayName, ""); banned {
			return gid, msg, false
		}
	}
	admin := s.n != nil && s.n.IsAdmin(gid.XUID, gid.DisplayName)
	if !takeSlot(admin) {
		return gid, fullMessage, false
	}
	if s.cfg.WorldPlayers != nil {
		s.cfg.WorldPlayers.Add(1)
	}
	if s.log != nil {
		s.log.Info("network: split-screen player passed through", "player", gid.DisplayName, "sub", sub, "xuid", gid.XUID)
	}
	return gid, "", true
}

// ptForgetLocked ends a passed-through guest: it is no longer routed or counted. s.mu must be held.
func (s *switchSession) ptForgetLocked(sub byte) {
	if _, ok := s.ptGuests[sub]; !ok {
		return
	}
	delete(s.ptGuests, sub)
	delete(s.ptItems, sub)
	delete(s.ptIDs, sub)
	delete(s.ptLogins, sub)
	noteBedrock(-1)
	if s.cfg.WorldPlayers != nil {
		s.cfg.WorldPlayers.Add(-1)
	}
}

// ptForgetAll ends every passed-through guest, as the host leaves.
func (s *switchSession) ptForgetAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.ptGuests {
		s.ptForgetLocked(sub)
	}
}

// ptPlayers returns the identities of the passed-through guests.
func (s *switchSession) ptPlayers() []login.IdentityData {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]login.IdentityData, 0, len(s.ptIDs))
	for _, id := range s.ptIDs {
		out = append(out, id)
	}
	return out
}

// guestServerCommand reports the server a passed-through guest's /server command names, if it is one
// of the network's.
func (s *switchSession) guestServerCommand(raw []byte) (ServerConfig, bool) {
	_, payload, err := splitPacket(raw)
	if err != nil || s.n == nil {
		return ServerConfig{}, false
	}
	var pk packet.CommandRequest
	if decodeInto(&pk, payload) != nil {
		return ServerConfig{}, false
	}
	f := strings.Fields(pk.CommandLine)
	if len(f) != 2 || !strings.EqualFold(f[0], "/server") {
		return ServerConfig{}, false
	}
	target := s.n.server(f[1])
	if target == nil {
		return ServerConfig{}, false
	}
	return *target, true
}

// guestMovesAlone moves a passed-through guest to target on its own, as one of Beacon's own guests: it
// stays there until the host's next move brings it back. A guest can only be passed through on the
// host's own server, so a target that handles guests itself is refused.
func (s *switchSession) guestMovesAlone(sub byte, target ServerConfig) {
	s.mu.Lock()
	raw := s.ptLogins[sub]
	host := ""
	if s.cur != nil {
		host = s.cur.server.Name
	}
	s.mu.Unlock()
	if strings.EqualFold(target.Name, host) {
		return
	}
	if passthroughServer(target.Name) {
		_ = ptWriteClient(s, subClientHeader(encodePacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "A split-screen player can only go to " + target.Name + " with the first player."}), sub))
		return
	}
	req := subClientLoginRequest(raw)
	if req == nil {
		return
	}
	// Leave the server the guest is on, then join target through Beacon.
	s.mu.Lock()
	l := s.ptGuests[sub]
	s.ptForgetLocked(sub)
	s.mu.Unlock()
	if l != nil {
		_ = ptWriteServer(l, [][]byte{withHeaderBits(encodePacket(&packet.Disconnect{HideDisconnectionScreen: true}), uint32(sub)<<12)})
	}
	go ptStartGuest(s, sub, req, target.Name)
}
