package bridge

import (
	"testing"
	"time"
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
