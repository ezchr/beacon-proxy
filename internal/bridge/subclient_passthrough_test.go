package bridge

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func withPassthrough(t *testing.T, v string) {
	old := SubClientPassthrough
	SubClientPassthrough = parsePassthrough(v)
	t.Cleanup(func() { SubClientPassthrough = old })
}

// subPacket is a server packet for sub-client sub (bits 10-11).
func subPacket(pk packet.Packet, sub byte) []byte {
	return subClientHeader(encodePacket(pk), sub)
}

// guestPacket is a client packet from sub-client sub, as a console writes it (bits 12-13).
func guestPacket(pk packet.Packet, sub byte) []byte {
	return withHeaderBits(encodePacket(pk), uint32(sub)<<12)
}

func ptTestSession(server string) (*switchSession, *link) {
	l := &link{server: ServerConfig{Name: server}, rw: newRewriter(1, 1, 1, 1, nil)}
	s := &switchSession{log: slog.Default(), cur: l, pool: packet.NewClientPool()}
	return s, l
}

func TestPassthroughServers(t *testing.T) {
	withPassthrough(t, " MiniGames , lobby")
	if !passthroughServer("minigames") || !passthroughServer("Lobby") || passthroughServer("skygen") {
		t.Fatal("server list not read")
	}
	withPassthrough(t, "*")
	if !passthroughServer("anything") {
		t.Fatal("* does not take all servers")
	}
	withPassthrough(t, "")
	if passthroughServer("minigames") {
		t.Fatal("empty list takes a server")
	}
}

func TestPassthroughOnlyOnListedServers(t *testing.T) {
	withPassthrough(t, "minigames")
	login := guestPacket(&packet.SubClientLogin{ConnectionRequest: []byte{1}}, 1)

	s, _ := ptTestSession("skygen")
	if s.isPassthroughGuest(1, packet.IDSubClientLogin) {
		t.Fatal("guest on skygen passed through: it must get Beacon's own handling")
	}

	s, l := ptTestSession("minigames")
	if own := s.routeSubClients([][]byte{login}); len(own) != 1 {
		t.Fatalf("SubClientLogin on a passthrough server not kept for the backend: %d", len(own))
	}
	out := s.passGuestToServer(l, login, packet.IDSubClientLogin)
	if out == nil || clientSubClient(out) != 1 || !bytes.Equal(out, login) {
		t.Fatal("SubClientLogin not passed on raw with its sub-client id")
	}
	move := guestPacket(&packet.Text{TextType: packet.TextTypeChat, Message: "hi"}, 1)
	if own := s.routeSubClients([][]byte{move}); len(own) != 1 {
		t.Fatal("passed-through guest's packet routed to Beacon's guests")
	}
	if out := s.passGuestToServer(l, move, packet.IDText); headerValue(out)>>12&3 != 1 || headerValue(out)>>10&3 != 0 {
		t.Fatal("guest packet lost its sub-client id")
	}
	// A sub-client that never logged in is dropped.
	if out := s.passGuestToServer(l, guestPacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "y"}, 2), packet.IDText); out != nil {
		t.Fatal("packet of an unknown sub-client passed on")
	}
}

func TestPassthroughServerPacketsForGuest(t *testing.T) {
	withPassthrough(t, "minigames")
	s, l := ptTestSession("minigames")
	s.ptUnion = &union{
		blocks: []protocol.BlockEntry{{Name: "zid:test"}},
		items:  &packet.ItemRegistry{Items: []protocol.ItemEntry{{Name: "minecraft:stick", RuntimeID: 5}}},
		actors: &packet.AvailableActorIdentifiers{},
	}
	text := subPacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "x"}, 2)
	id, payload, _ := splitPacket(text)
	if out := s.passGuestToClient(l, text, id, payload); !bytes.Equal(out, text) {
		t.Fatal("guest packet not passed on unchanged")
	}

	sgRaw := subPacket(&packet.StartGame{UseBlockNetworkIDHashes: true, EntityRuntimeID: 7}, 2)
	id, payload, _ = splitPacket(sgRaw)
	out := s.passGuestToClient(l, sgRaw, id, payload)
	if subClientOf(out) != 2 {
		t.Fatal("StartGame lost its sub-client id")
	}
	var sg packet.StartGame
	_, p, _ := splitPacket(out)
	if err := decodeInto(&sg, p); err != nil || len(sg.Blocks) != 1 || sg.UseBlockNetworkIDHashes || sg.EntityRuntimeID != 7 {
		t.Fatalf("guest StartGame not given the union's blocks: %v %+v", err, sg.Blocks)
	}

	reg := subPacket(&packet.ItemRegistry{}, 2)
	id, payload, _ = splitPacket(reg)
	first := s.passGuestToClient(l, reg, id, payload)
	var items packet.ItemRegistry
	_, p, _ = splitPacket(first)
	if subClientOf(first) != 2 || decodeInto(&items, p) != nil || len(items.Items) != 1 {
		t.Fatal("guest item registry is not the union's")
	}
	if s.passGuestToClient(l, reg, id, payload) != nil {
		t.Fatal("second item registry passed on")
	}
}

func TestWithSub(t *testing.T) {
	orig := subPacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "a"}, 3)
	out := withSub(orig, encodePacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "b"}))
	if subClientOf(out) != 3 {
		t.Fatal("sub-client id not restored")
	}
	host := encodePacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "a"})
	if !bytes.Equal(withSub(host, host), host) {
		t.Fatal("host packet changed")
	}
}

func TestClientSubClientBits(t *testing.T) {
	// A console writes the guest in bits 12-13 and 0 in bits 10-11.
	raw := guestPacket(&packet.SubClientLogin{ConnectionRequest: []byte{1}}, 2)
	if clientSubClient(raw) != 2 || subClientOf(raw) != 0 {
		t.Fatalf("client sub %d, bits 10-11 %d", clientSubClient(raw), subClientOf(raw))
	}
	if id, _, _ := splitPacket(raw); id != packet.IDSubClientLogin {
		t.Fatalf("id %d", id)
	}
	// Older senders with the guest in bits 10-11 are still read.
	if clientSubClient(subPacket(&packet.SubClientLogin{}, 3)) != 3 {
		t.Fatal("bits 10-11 not read as fallback")
	}
	// withSub keeps bits 12-13 through a rewrite.
	out := withSub(raw, encodePacket(&packet.SubClientLogin{ConnectionRequest: []byte{9}}))
	if headerValue(out)&^0x3FF != 2<<12 {
		t.Fatalf("bits after rewrite %#x", headerValue(out))
	}
}

// After a move the host link swaps the host's server and client IDs. A guest whose server ID is one of
// them gets the swapped one in its StartGame, as its later packets are swapped.
func TestPassthroughGuestStartGameSwapsIDs(t *testing.T) {
	withPassthrough(t, "minigames")
	s, _ := ptTestSession("minigames")
	l := &link{server: ServerConfig{Name: "minigames"}, rw: newRewriter(1, 1, 3, 3, nil)}
	s.ptUnion = &union{items: &packet.ItemRegistry{}, actors: &packet.AvailableActorIdentifiers{}}
	raw := subPacket(&packet.StartGame{EntityRuntimeID: 3, EntityUniqueID: 3}, 1)
	id, payload, _ := splitPacket(raw)
	out := s.passGuestToClient(l, raw, id, payload)
	var sg packet.StartGame
	_, p, _ := splitPacket(out)
	if err := decodeInto(&sg, p); err != nil || sg.EntityRuntimeID != 1 || sg.EntityUniqueID != 1 {
		t.Fatalf("guest StartGame IDs %d/%d, want 1/1 (err %v)", sg.EntityRuntimeID, sg.EntityUniqueID, err)
	}
	// And its SetLocalPlayerAsInitialised with the client ID goes back as the server one.
	back := s.passGuestToServer(l, guestPacket(&packet.SubClientLogin{}, 1), packet.IDSubClientLogin)
	_ = back
	init := guestPacket(&packet.SetLocalPlayerAsInitialised{EntityRuntimeID: 1}, 1)
	got := s.passGuestToServer(l, init, packet.IDSetLocalPlayerAsInitialised)
	var pk packet.SetLocalPlayerAsInitialised
	_, p, _ = splitPacket(got)
	if err := decodeInto(&pk, p); err != nil || pk.EntityRuntimeID != 3 {
		t.Fatalf("SetLocalPlayerAsInitialised to server %d (err %v), want 3", pk.EntityRuntimeID, err)
	}
}
