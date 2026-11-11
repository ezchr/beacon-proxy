package bridge

import (
	"bytes"
	"sync"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// followRecorder replaces how followHost moves guests and records what it did.
type followRecorder struct {
	mu      sync.Mutex
	written map[*link][][]byte
	started []string
	toPass  []byte
	done    chan struct{}
}

func recordFollow(t *testing.T) *followRecorder {
	r := &followRecorder{written: map[*link][][]byte{}, done: make(chan struct{}, 8)}
	ow, os, op := ptWriteServer, ptStartGuest, ptToPassthrough
	ptWriteServer = func(l *link, pks [][]byte) error {
		r.mu.Lock()
		r.written[l] = append(r.written[l], pks...)
		r.mu.Unlock()
		return nil
	}
	ptStartGuest = func(_ *switchSession, sub byte, req []byte, server string) {
		r.mu.Lock()
		r.started = append(r.started, server)
		r.mu.Unlock()
		r.done <- struct{}{}
	}
	ptToPassthrough = func(_ *switchSession, g *switchSession, server ServerConfig) {
		r.mu.Lock()
		r.toPass = append(r.toPass, g.sub)
		r.mu.Unlock()
		r.done <- struct{}{}
	}
	t.Cleanup(func() { ptWriteServer, ptStartGuest, ptToPassthrough = ow, os, op })
	return r
}

func followTestSession(t *testing.T, server string) (*switchSession, *link) {
	oldDir := PlayerCountDir
	PlayerCountDir = t.TempDir()
	t.Cleanup(func() { PlayerCountDir = oldDir })
	s, l := ptTestSession(server)
	s.id = login.IdentityData{XUID: "2535400000000000", DisplayName: "Host"}
	return s, l
}

// Host moves between two servers that handle guests themselves: the guest logs in on the new one with
// its saved SubClientLogin and stays counted once.
func TestFollowPassthroughToPassthrough(t *testing.T) {
	withPassthrough(t, "minigames,lobby")
	r := recordFollow(t)
	s, l := followTestSession(t, "minigames")
	login := guestPacket(&packet.SubClientLogin{ConnectionRequest: []byte{7}}, 1)
	s.passGuestToServer(l, login, packet.IDSubClientLogin)
	counted := onlineBedrock.Load()

	nl := &link{server: ServerConfig{Name: "lobby"}, rw: newRewriter(1, 1, 1, 1, nil)}
	s.cur = nl
	s.followHost("minigames", nl.server)
	if got := r.written[nl]; len(got) != 1 || !bytes.Equal(got[0], login) {
		t.Fatalf("saved login not replayed on the new server: %d packets", len(got))
	}
	if s.ptGuests[1] != nl || onlineBedrock.Load() != counted {
		t.Fatal("guest not moved to the new link, or counted again")
	}
	// Its packets now go to the new link.
	if out := s.passGuestToServer(nl, guestPacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "x"}, 1), packet.IDText); out == nil {
		t.Fatal("guest packet not passed on after the move")
	}
}

// Host moves from a server that handles guests itself to one that does not: the guest becomes one of
// Beacon's own, joining the host's new server.
func TestFollowPassthroughToBeacon(t *testing.T) {
	withPassthrough(t, "minigames")
	r := recordFollow(t)
	s, l := followTestSession(t, "minigames")
	s.passGuestToServer(l, guestPacket(&packet.SubClientLogin{ConnectionRequest: []byte{7}}, 1), packet.IDSubClientLogin)
	before := onlineBedrock.Load()

	nl := &link{server: ServerConfig{Name: "skygen"}, rw: newRewriter(1, 1, 1, 1, nil)}
	s.cur = nl
	s.followHost("minigames", nl.server)
	<-r.done
	if len(r.started) != 1 || r.started[0] != "skygen" {
		t.Fatalf("guest not started on the host's server: %v", r.started)
	}
	if len(s.ptGuests) != 0 || onlineBedrock.Load() != before-1 {
		t.Fatal("passthrough guest not ended (its own session counts it again)")
	}
}

// Host moves from a Beacon-handled server to one that handles guests itself: Beacon's own guest goes
// over to the server.
func TestFollowBeaconToPassthrough(t *testing.T) {
	withPassthrough(t, "minigames")
	r := recordFollow(t)
	s, _ := followTestSession(t, "skygen")
	g := &switchSession{host: s, sub: 1, subRequest: []byte{7}}
	s.guests = map[byte]*switchSession{1: g}
	nl := &link{server: ServerConfig{Name: "minigames"}, rw: newRewriter(1, 1, 1, 1, nil)}
	s.cur = nl
	s.followHost("skygen", nl.server)
	<-r.done
	if len(r.toPass) != 1 || r.toPass[0] != 1 {
		t.Fatalf("own guest not moved over: %v", r.toPass)
	}
}

// A passed-through guest that runs /server goes alone, as one of Beacon's own guests; one naming
// another server that handles guests itself is told it can't.
func TestPassthroughGuestServerCommand(t *testing.T) {
	withPassthrough(t, "minigames,lobby")
	r := recordFollow(t)
	var sent [][]byte
	ow := ptWriteClient
	ptWriteClient = func(_ *switchSession, raw []byte) error { sent = append(sent, raw); return nil }
	t.Cleanup(func() { ptWriteClient = ow })
	s, l := followTestSession(t, "minigames")
	s.n = &Network{Dir: t.TempDir(), Servers: []ServerConfig{{Name: "minigames"}, {Name: "lobby"}, {Name: "pvp"}}}
	s.passGuestToServer(l, guestPacket(&packet.SubClientLogin{ConnectionRequest: []byte{7}}, 1), packet.IDSubClientLogin)

	cmd := func(line string) []byte {
		return s.passGuestToServer(l, guestPacket(&packet.CommandRequest{CommandLine: line, CommandOrigin: protocol.CommandOrigin{Origin: protocol.CommandOriginPlayer}}, 1), packet.IDCommandRequest)
	}
	if out := cmd("/server lobby"); out != nil || len(sent) != 1 {
		t.Fatalf("guest /server to another passthrough server: out %v, %d messages", out != nil, len(sent))
	}
	if out := cmd("/server pvp"); out != nil {
		t.Fatal("guest /server pvp passed to the backend")
	}
	<-r.done
	if len(r.started) != 1 || r.started[0] != "pvp" || len(s.ptGuests) != 0 {
		t.Fatalf("guest did not go alone to pvp: %v", r.started)
	}
	if got := r.written[l]; len(got) != 1 {
		t.Fatalf("old server not told the guest left: %d packets", len(got))
	}
}
