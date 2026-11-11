package bridge

import (
	"testing"
	"time"
)

// An address that joins again before its entry is forgotten is still forgotten later.
func TestLastLoginForgottenAfterRejoin(t *testing.T) {
	old := currentLimits()
	defer SetLimits(old)
	SetLimits(Limits{LoginInterval: 40 * time.Millisecond})
	const ip = "198.51.100.77"
	lastLogin.Delete(ip)
	if loginTooSoon(ip) {
		t.Fatal("first join refused")
	}
	time.Sleep(25 * time.Millisecond)
	loginTooSoon(ip) // again before the first timer runs
	time.Sleep(25 * time.Millisecond)
	if _, ok := lastLogin.Load(ip); !ok {
		t.Fatal("entry dropped while the address still has to wait")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := lastLogin.Load(ip); !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("entry kept forever")
}

// Rejoin targets past their window are pruned; fresh ones stay.
func TestPruneRejoins(t *testing.T) {
	n := &Network{}
	n.rememberRejoin("1", "pvp")
	n.rejoinTo.Store("2", rejoinTarget{server: "lobby", until: time.Now().Add(-time.Second)})
	n.pruneRejoins(time.Now())
	if _, ok := n.rejoinTo.Load("2"); ok {
		t.Error("expired target kept")
	}
	if _, ok := n.rejoinTo.Load("1"); !ok {
		t.Error("fresh target pruned")
	}
}
