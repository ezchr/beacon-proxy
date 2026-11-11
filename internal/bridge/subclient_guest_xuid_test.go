package bridge

import (
	"strconv"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// Floodgate reads XUIDs as a long: a derived one must fit an int64.
func TestGuestXUIDFitsInt64(t *testing.T) {
	for _, x := range []string{"", "1", "2535400000000000", "2535499999999999"} {
		for sub := byte(1); sub <= 3; sub++ {
			id, derived := guestIdentity(login.IdentityData{XUID: x, DisplayName: "Host"}, sub, login.IdentityData{}, false)
			if !derived {
				t.Fatal("not derived")
			}
			if v, err := strconv.ParseInt(id.XUID, 10, 64); err != nil || v < 9_000_000_000_000_000_000 {
				t.Fatalf("XUID %s does not fit an int64 from 9e18 up: %v", id.XUID, err)
			}
		}
	}
}
