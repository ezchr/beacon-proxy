package bridge

import (
	"bytes"
	"context"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Packets to a split-screen player carry its id where BDS's receive reads the sub-client (bits 10-11),
// the packet id stays, and a packet passed on to a backend loses the id again.
func TestSubClientHeaderBothWays(t *testing.T) {
	for _, id := range []uint32{packet.IDPlayStatus, packet.IDStartGame, packet.IDPlayerAuthInput, packet.IDSubChunk} {
		raw := encodePacket(&packet.Text{Message: "x"})
		_, payload, _ := splitPacket(raw)
		var b bytes.Buffer
		_ = (&packet.Header{PacketID: id}).Write(&b)
		b.Write(payload)
		for _, sub := range []byte{0, 1, 2, 3} {
			out := subClientHeader(b.Bytes(), sub)
			var h packet.Header
			buf := bytes.NewBuffer(out)
			if err := h.Read(buf); err != nil {
				t.Fatal(err)
			}
			if h.PacketID != id || h.SenderSubClient != sub || h.TargetSubClient != 0 {
				t.Fatalf("id %d sub %d: header %+v", id, sub, h)
			}
			if subClientOf(out) != sub {
				t.Fatalf("subClientOf = %d, want %d", subClientOf(out), sub)
			}
			if !bytes.Equal(buf.Bytes(), payload) {
				t.Fatal("payload changed")
			}
			if back := subClientHeader(out, 0); subClientOf(back) != 0 || !bytes.Equal(back, b.Bytes()) {
				t.Fatalf("back to the main player: %x, want %x", back, b.Bytes())
			}
		}
	}
	// A client's packet with bits 12-13 set as well (the packet's own sub-client id) comes out clean.
	var b bytes.Buffer
	_ = (&packet.Header{PacketID: packet.IDText, SenderSubClient: 2, TargetSubClient: 2}).Write(&b)
	if out := subClientHeader(b.Bytes(), 0); len(out) != 1 || out[0] != byte(packet.IDText) {
		t.Fatalf("cleared header %x", out)
	}
}

func TestGuestIdentity(t *testing.T) {
	host := login.IdentityData{XUID: "2535440792904888", DisplayName: "Steve1234", Identity: "00000000-0000-3000-8000-000000000001"}
	a, derived := guestIdentity(host, 1, login.IdentityData{DisplayName: "Player 2"}, false)
	b, _ := guestIdentity(host, 1, login.IdentityData{}, false)
	c, _ := guestIdentity(host, 2, login.IdentityData{}, false)
	if !derived || a.XUID != b.XUID || a.Identity != b.Identity || a.DisplayName != "Steve1234_1" {
		t.Fatalf("not stable: %+v %+v", a, b)
	}
	if a.XUID == c.XUID || a.Identity == c.Identity || a.DisplayName == c.DisplayName || a.XUID == host.XUID {
		t.Fatalf("not distinct: %+v %+v", a, c)
	}
	if len(a.XUID) != 19 || a.XUID[0] != '9' {
		t.Fatalf("xuid %q", a.XUID)
	}
	own := login.IdentityData{XUID: "2535411111111111", DisplayName: "Sibling"}
	if got, d := guestIdentity(host, 1, own, true); d || got != own {
		t.Fatalf("a guest with its own Xbox account lost it: %+v", got)
	}
	if _, d := guestIdentity(host, 1, host, true); !d {
		t.Fatal("a guest signed in as the host played as the host")
	}
	if _, d := guestIdentity(host, 1, own, false); !d {
		t.Fatal("an unverified guest kept the identity it claimed")
	}
	if n := guestName("A Very Long Gamertag!", 3); n != "AVeryLongGame_3" {
		t.Fatalf("name %q", n)
	}
}

// The host's reader keeps its own packets, hands a guest its packets (as a main player's), drops those
// of a sub-client not logged in, and a guest's Disconnect ends only that guest.
func TestRouteSubClients(t *testing.T) {
	hostCtx, hostCancel := context.WithCancelCause(context.Background())
	defer hostCancel(nil)
	host := &switchSession{ctx: hostCtx}
	gctx, gcancel := context.WithCancelCause(hostCtx)
	g := &switchSession{ctx: gctx, cancel: gcancel, host: host, sub: 1, inbox: make(chan []byte, 8)}
	host.guests = map[byte]*switchSession{1: g}
	mk := func(pk packet.Packet, sub byte) []byte { return subClientHeader(encodePacket(pk), sub) }
	own := host.routeSubClients([][]byte{
		mk(&packet.Text{Message: "host"}, 0),
		mk(&packet.Text{Message: "guest"}, 1),
		mk(&packet.PlayerSkin{}, 2), // sub-client 2 never logged in
		mk(&packet.Text{Message: "host2"}, 0),
	})
	if len(own) != 2 {
		t.Fatalf("host kept %d packets", len(own))
	}
	if len(g.inbox) != 1 {
		t.Fatalf("guest got %d packets", len(g.inbox))
	}
	raw := <-g.inbox
	id, payload, _ := splitPacket(raw)
	var txt packet.Text
	if subClientOf(raw) != 0 || id != packet.IDText || decodeInto(&txt, payload) != nil || txt.Message != "guest" {
		t.Fatalf("guest packet %x", raw)
	}
	host.routeSubClients([][]byte{mk(&packet.Disconnect{}, 1)})
	if gctx.Err() == nil || hostCtx.Err() != nil {
		t.Fatalf("guest left: guest ctx %v, host ctx %v", gctx.Err(), hostCtx.Err())
	}
	// The host's connection ending ends its guests (their contexts are the host's children).
	g2ctx, g2cancel := context.WithCancelCause(hostCtx)
	defer g2cancel(nil)
	hostCancel(nil)
	if g2ctx.Err() == nil {
		t.Fatal("guest outlived the host")
	}
}

// A guest reads what the host's reader handed it, and stops when it ends.
func TestGuestReadPackets(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	g := &switchSession{ctx: ctx, host: &switchSession{}, inbox: make(chan []byte, 8)}
	g.inbox <- []byte{1}
	g.inbox <- []byte{2}
	pks, err := g.readPackets()
	if err != nil || len(pks) != 2 {
		t.Fatalf("%d packets, %v", len(pks), err)
	}
	cancel(nil)
	if _, err := g.readPackets(); err == nil {
		t.Fatal("read after the guest ended")
	}
}

// A guest's chat line and skin carry the guest's identity, whatever the console put in them.
func TestGuestPacketIdentity(t *testing.T) {
	g := &switchSession{}
	g.id, _ = guestIdentity(login.IdentityData{XUID: "2535440792904888", DisplayName: "Host"}, 1, login.IdentityData{}, false)
	_, payload, _ := splitPacket(g.guestPacket(encodePacket(&packet.Text{TextType: packet.TextTypeChat, SourceName: "Player 2", Message: "hi", XUID: "2535440792904888"})))
	var txt packet.Text
	if decodeInto(&txt, payload) != nil || txt.XUID != g.id.XUID || txt.SourceName != g.id.DisplayName || txt.Message != "hi" {
		t.Fatalf("text %+v", txt)
	}
	_, payload, _ = splitPacket(g.guestPacket(encodePacket(&packet.PlayerSkin{})))
	var skin packet.PlayerSkin
	if decodeInto(&skin, payload) != nil || skin.UUID.String() != g.id.Identity {
		t.Fatalf("skin uuid %v, want %s", skin.UUID, g.id.Identity)
	}
}
