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

func subPacket(pk packet.Packet, sub byte) []byte {
	return subClientHeader(encodePacket(pk), sub)
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
	login := subPacket(&packet.SubClientLogin{ConnectionRequest: []byte{1}}, 1)

	s, _ := ptTestSession("skygen")
	if s.isPassthroughGuest(1, packet.IDSubClientLogin) {
		t.Fatal("guest on skygen passed through: it must get Beacon's own handling")
	}

	s, l := ptTestSession("minigames")
	if own := s.routeSubClients([][]byte{login}); len(own) != 1 {
		t.Fatalf("SubClientLogin on a passthrough server not kept for the backend: %d", len(own))
	}
	out := s.passGuestToServer(l, login, packet.IDSubClientLogin)
	if out == nil || subClientOf(out) != 1 || !bytes.Equal(out, login) {
		t.Fatal("SubClientLogin not passed on raw with its sub-client id")
	}
	move := subPacket(&packet.Text{TextType: packet.TextTypeChat, Message: "hi"}, 1)
	if own := s.routeSubClients([][]byte{move}); len(own) != 1 {
		t.Fatal("passed-through guest's packet routed to Beacon's guests")
	}
	if out := s.passGuestToServer(l, move, packet.IDText); subClientOf(out) != 1 {
		t.Fatal("guest packet lost its sub-client id")
	}
	// A sub-client that never logged in is dropped.
	if out := s.passGuestToServer(l, subPacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "y"}, 2), packet.IDText); out != nil {
		t.Fatal("packet of an unknown sub-client passed on")
	}
}

func TestPassthroughGuestDisconnectedOnHostMove(t *testing.T) {
	withPassthrough(t, "minigames,lobby")
	var sent [][]byte
	old := ptWriteClient
	ptWriteClient = func(_ *switchSession, raw []byte) error { sent = append(sent, raw); return nil }
	t.Cleanup(func() { ptWriteClient = old })

	s, l := ptTestSession("minigames")
	s.passGuestToServer(l, subPacket(&packet.SubClientLogin{}, 1), packet.IDSubClientLogin)

	// The host moves on, to another passthrough server: the guest's login was on the old one.
	s.cur = &link{server: ServerConfig{Name: "lobby"}, rw: newRewriter(1, 1, 1, 1, nil)}
	if out := s.passGuestToServer(s.cur, subPacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "y"}, 1), packet.IDText); out != nil {
		t.Fatal("guest packet sent to a server the guest never logged in to")
	}
	if len(sent) != 1 || subClientOf(sent[0]) != 1 {
		t.Fatalf("guest not told it left: %d packets", len(sent))
	}
	if id, _, _ := splitPacket(sent[0]); id != packet.IDDisconnect {
		t.Fatalf("sent packet %d, want Disconnect", id)
	}
	// It can log in again on the new server, in its mode.
	if !s.isPassthroughGuest(1, packet.IDSubClientLogin) {
		t.Fatal("guest cannot log in again on the passthrough server")
	}
	// On a server that is not listed, its next login goes to Beacon's own handling.
	s.cur = &link{server: ServerConfig{Name: "skygen"}, rw: newRewriter(1, 1, 1, 1, nil)}
	if s.isPassthroughGuest(1, packet.IDSubClientLogin) {
		t.Fatal("guest login on skygen passed through")
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
