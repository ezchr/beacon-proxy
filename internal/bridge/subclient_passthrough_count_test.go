package bridge

import (
	"sync/atomic"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// A passed-through guest counts as a player while it is on: in the online count, its Friends-tab
// world and the player list, and it is refused when the server is full.
func TestPassthroughGuestCounted(t *testing.T) {
	withPassthrough(t, "minigames")
	oldDir := PlayerCountDir
	PlayerCountDir = t.TempDir()
	t.Cleanup(func() { PlayerCountDir = oldDir })
	var sent [][]byte
	oldWrite := ptWriteClient
	ptWriteClient = func(_ *switchSession, raw []byte) error { sent = append(sent, raw); return nil }
	t.Cleanup(func() { ptWriteClient = oldWrite })

	s, l := ptTestSession("minigames")
	s.id = login.IdentityData{XUID: "2535400000000000", DisplayName: "Host"}
	world := new(atomic.Int64)
	s.cfg.WorldPlayers = world
	before := onlineBedrock.Load()

	if out := s.passGuestToServer(l, guestPacket(&packet.SubClientLogin{}, 1), packet.IDSubClientLogin); out == nil {
		t.Fatal("guest login not passed on")
	}
	if onlineBedrock.Load() != before+1 || world.Load() != 1 {
		t.Fatalf("guest not counted: online %d->%d world %d", before, onlineBedrock.Load(), world.Load())
	}
	if ps := s.ptPlayers(); len(ps) != 1 || ps[0].DisplayName != "Host_1" {
		t.Fatalf("guest not listed: %+v", ps)
	}
	// A second SubClientLogin of the same guest is not counted again.
	s.passGuestToServer(l, guestPacket(&packet.SubClientLogin{}, 1), packet.IDSubClientLogin)
	if onlineBedrock.Load() != before+1 {
		t.Fatal("guest counted twice")
	}
	s.passGuestToServer(l, guestPacket(&packet.Disconnect{}, 1), packet.IDDisconnect)
	if onlineBedrock.Load() != before || world.Load() != 0 || len(s.ptPlayers()) != 0 {
		t.Fatal("guest still counted after leaving")
	}

	// Full: refused with a message to its sub-client, not counted.
	noteBedrock(1) // someone else holds the last slot
	SetPlayerCap(int(onlineBedrock.Load()))
	t.Cleanup(func() { SetPlayerCap(0) })
	if out := s.passGuestToServer(l, guestPacket(&packet.SubClientLogin{}, 2), packet.IDSubClientLogin); out != nil {
		t.Fatal("guest let in on a full server")
	}
	if len(sent) != 1 || subClientOf(sent[0]) != 2 || onlineBedrock.Load() != before+1 {
		t.Fatalf("full server: %d packets sent, online %d", len(sent), onlineBedrock.Load())
	}

	// The host leaving ends its guests' count.
	noteBedrock(-1)
	SetPlayerCap(0)
	s.passGuestToServer(l, guestPacket(&packet.SubClientLogin{}, 1), packet.IDSubClientLogin)
	s.ptForgetAll()
	if onlineBedrock.Load() != before {
		t.Fatal("guest still counted after the host left")
	}
}
