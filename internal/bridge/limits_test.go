package bridge

import (
	"net"
	"testing"
)

func TestHoldSessionCapsPlayersPerIP(t *testing.T) {
	SetLimits(Limits{SessionsPerIP: 2})
	defer SetLimits(Limits{})

	releaseA, ok := holdSession("203.0.113.1", "1")
	if !ok {
		t.Fatal("first player refused")
	}
	if _, ok := holdSession("203.0.113.1", "2"); !ok {
		t.Fatal("second player refused")
	}
	if _, ok := holdSession("203.0.113.1", "3"); ok {
		t.Fatal("third player let in past the cap")
	}
	// A player joining again replaces their own session, so the cap does not stop them.
	releaseAgain, ok := holdSession("203.0.113.1", "1")
	if !ok {
		t.Fatal("player joining again refused")
	}
	if _, ok := holdSession("203.0.113.2", "3"); !ok {
		t.Fatal("player from another address refused")
	}
	releaseA()
	if _, ok := holdSession("203.0.113.1", "3"); ok {
		t.Fatal("player 1 still has a session, but the cap let a third player in")
	}
	releaseAgain()
	if _, ok := holdSession("203.0.113.1", "3"); !ok {
		t.Fatal("player refused after another left")
	}
}

func TestHoldSessionOffByDefault(t *testing.T) {
	SetLimits(Limits{})
	for i := range 10 {
		if _, ok := holdSession("203.0.113.9", string(rune('a'+i))); !ok {
			t.Fatal("refused with no cap")
		}
	}
}

type pendingAddr string

func (pendingAddr) Network() string  { return "nethernet" }
func (a pendingAddr) String() string { return string(a) }

// Connections not logged in yet are capped per address at sessions-per-ip; a relayed one counts
// against the player's own address, not the shared relay's.
func TestHoldPending(t *testing.T) {
	defer SetLimits(currentLimits())
	SetLimits(Limits{SessionsPerIP: 2})
	far := pendingAddr("1 (2) (udp4 prflx 203.0.113.9:1234 related :0 (resolved: 203.0.113.9:1234))")
	r1, ok1 := holdPending(far)
	r2, ok2 := holdPending(far)
	if !ok1 || !ok2 {
		t.Fatal("first two refused")
	}
	if _, ok := holdPending(far); ok {
		t.Fatal("third let in over the cap")
	}
	r1()
	r1() // twice does nothing
	r3, ok := holdPending(far)
	if !ok {
		t.Fatal("refused after one was released")
	}
	if _, ok := holdPending(far); ok {
		t.Fatal("double release freed two slots")
	}
	r2()
	r3()
	if len(pending) != 0 {
		t.Fatalf("left over: %v", pending)
	}
	// Two players behind one TURN relay each get their own count.
	a := pendingAddr("1 (2) (udp4 relay 20.202.68.115:55771 related 198.51.100.1:22582 (resolved: 20.202.68.115:55771))")
	b := pendingAddr("1 (2) (udp4 relay 20.202.68.115:55772 related 198.51.100.2:22582 (resolved: 20.202.68.115:55772))")
	var rel []func()
	for _, addr := range []pendingAddr{a, a, b, b} {
		r, ok := holdPending(addr)
		if !ok {
			t.Fatalf("relayed %s refused", addr)
		}
		rel = append(rel, r)
	}
	if _, ok := holdPending(a); ok {
		t.Fatal("relayed player over the cap let in")
	}
	if got := pendingIP(a); !got.Equal(net.ParseIP("198.51.100.1")) {
		t.Fatalf("relay counted against %v", got)
	}
	for _, r := range rel {
		r()
	}
	// Off when sessions-per-ip is 0; loopback is never capped.
	SetLimits(Limits{})
	for i := 0; i < 10; i++ {
		if _, ok := holdPending(far); !ok {
			t.Fatal("capped with the limit off")
		}
	}
	SetLimits(Limits{SessionsPerIP: 1})
	near := pendingAddr("1 (2) (udp4 host 127.0.0.1:1 (resolved: 127.0.0.1:1))")
	for i := 0; i < 5; i++ {
		if _, ok := holdPending(near); !ok {
			t.Fatal("this machine capped")
		}
	}
}
