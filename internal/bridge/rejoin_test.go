package bridge

import (
	"testing"
	"time"

	"github.com/ezchr/beacon-proxy/internal/proxy"
)

// A player sent to join again for new packs lands on the server they were moving to, once.
func TestRejoinLandsOnTheServerTheyWereMovingTo(t *testing.T) {
	n := &Network{}
	n.rememberRejoin("123", "skygen")
	if got := n.takeRejoin("123"); got != "skygen" {
		t.Fatalf("first join again: got %q, want skygen", got)
	}
	if got := n.takeRejoin("123"); got != "" {
		t.Fatalf("second join: got %q, want the usual order", got)
	}
	if got := n.takeRejoin("456"); got != "" {
		t.Fatalf("another player: got %q", got)
	}
	n.rejoinTo.Store("789", rejoinTarget{server: "skygen", until: time.Now().Add(-time.Second)})
	if got := n.takeRejoin("789"); got != "" {
		t.Fatalf("stale target: got %q", got)
	}
}

// A move asked for while the player is still joining (/send all) leaves them alone: their join
// already logs in to a server, and a second login there is refused as a duplicate.
func TestNoMoveWhileStillJoining(t *testing.T) {
	s := &switchSession{proto: proxy.SupportedProtocols()[0]}
	if err := s.requestSwitch(ServerConfig{Name: "skygen"}, ""); err == nil {
		t.Fatal("a player still on the pack screen was moved")
	}
}
