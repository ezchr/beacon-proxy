package bridge

import (
	"bytes"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func TestSubClientOf(t *testing.T) {
	for _, sub := range []byte{0, 1, 2, 3} {
		var buf bytes.Buffer
		h := packet.Header{PacketID: packet.IDPlayerAuthInput, SenderSubClient: sub}
		_ = h.Write(&buf)
		if got := subClientOf(buf.Bytes()); got != sub {
			t.Fatalf("sub-client %d read as %d", sub, got)
		}
		if id, _, _ := splitPacket(buf.Bytes()); id != packet.IDPlayerAuthInput {
			t.Fatalf("id %d", id)
		}
	}
}
