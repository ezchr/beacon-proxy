package bridge

import (
	"os"
	"strings"

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

// withSub gives out the sub-client id of orig when rewriting it lost the id.
func withSub(orig, out []byte) []byte {
	sub := subClientOf(orig)
	if sub == 0 || len(out) == 0 || subClientOf(out) == sub {
		return out
	}
	return subClientHeader(out, sub)
}

// passGuestToServer handles a guest's packet from the client in passthrough mode. It returns the
// packet to send to the server, or nil to send nothing.
func (s *switchSession) passGuestToServer(cur *link, raw []byte, id uint32) []byte {
	sub := subClientOf(raw)
	s.mu.Lock()
	if s.ptGuests == nil {
		s.ptGuests = map[byte]*link{}
	}
	if id == packet.IDSubClientLogin {
		s.ptGuests[sub] = cur
	}
	on, known := s.ptGuests[sub]
	if id == packet.IDDisconnect {
		delete(s.ptGuests, sub)
	}
	s.mu.Unlock()
	if !known || cur == nil {
		return nil
	}
	if on != cur {
		// The host moved: the guest's login was on the old server.
		s.mu.Lock()
		delete(s.ptGuests, sub)
		s.mu.Unlock()
		_ = ptWriteClient(s, subClientHeader(encodePacket(&packet.Disconnect{Message: "Split-screen players stay behind when the host changes servers. Join again."}), sub))
		return nil
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
		delete(s.ptGuests, sub)
		delete(s.ptItems, sub)
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
