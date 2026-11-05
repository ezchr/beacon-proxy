package proxy

import (
	"errors"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// A client of a version the relay does not serve gets the vanilla refusal, as from a Listener.
func TestUnsupportedClientRefused(t *testing.T) {
	for _, tc := range []struct {
		proto int32
		want  int32
	}{
		{1000, packet.PlayStatusLoginFailedClient},  // older than everything served
		{99999, packet.PlayStatusLoginFailedServer}, // newer
	} {
		a, b := newPipe()
		_, done := startRelay(a, false)
		client := NewProxyConn(b, false)
		if err := client.WritePacket(&packet.RequestNetworkSettings{ClientProtocol: tc.proto}); err != nil {
			t.Fatal(err)
		}
		pk, err := readOne(client)
		if err != nil {
			t.Fatal(err)
		}
		st, ok := pk.(*packet.PlayStatus)
		if !ok || st.Status != tc.want {
			t.Fatalf("protocol %d: got %#v, want play status %d", tc.proto, pk, tc.want)
		}
		if err := <-done; !errors.Is(err, ErrUnsupportedProtocol) {
			t.Fatalf("protocol %d: relay ended with %v", tc.proto, err)
		}
	}
}
