package bridge

import "testing"

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
