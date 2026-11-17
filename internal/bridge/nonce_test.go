package bridge

import (
	"io"
	"log/slog"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// A world join needs the nonce the world's session gave that account; the direct-IP door and a
// config without the check let every login on.
func TestWorldNonceOK(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	issued := func(xuid, nonce string) bool { return xuid == "1" && nonce == "abc" }
	world := Config{CheckNonce: issued}
	for _, c := range []struct {
		cfg   Config
		xuid  string
		nonce string
		want  bool
	}{
		{world, "1", "abc", true},
		{world, "1", "", false},
		{world, "2", "abc", false},
		{world, "1", "other", false},
		{Config{CheckNonce: issued, DirectIP: true}, "1", "", true},
		{Config{}, "1", "", true},
	} {
		if got := worldNonceOK(c.cfg, c.xuid, "x", login.ClientData{Nonce: c.nonce}, "r", log); got != c.want {
			t.Errorf("xuid %s nonce %q directIP %v: %v, want %v", c.xuid, c.nonce, c.cfg.DirectIP, got, c.want)
		}
	}
}
