package bridge

import (
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// BDS stopping kicks its players with an empty message and the shutdown reason: hold them. An
// empty kick with another reason stays a kick.
func TestHoldableShutdownReason(t *testing.T) {
	if !holdable(&kickError{code: packet.DisconnectReasonShutdown}, nil) {
		t.Fatal("a shutdown kick with no message should hold the player")
	}
	if !holdable(&kickError{code: packet.DisconnectReasonRequestServerShutdown}, nil) {
		t.Fatal("a requested shutdown should hold the player")
	}
	if holdable(&kickError{code: packet.DisconnectReasonKicked}, nil) {
		t.Fatal("an empty kick is not a shutdown")
	}
}
