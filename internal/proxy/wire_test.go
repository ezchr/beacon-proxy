package proxy

import (
	"bytes"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// testText is a Text of a made-up newer version with one field more.
type testText struct {
	packet.Text
	Extra string
}

func (pk *testText) Marshal(io protocol.IO) {
	pk.Text.Marshal(io)
	io.String(&pk.Extra)
}

type testProto struct{}

func (testProto) ID() int32   { return 9999 }
func (testProto) Ver() string { return "9.9.9" }
func (testProto) Packets(listener bool) packet.Pool {
	pool := packet.NewServerPool()
	if listener {
		pool = packet.NewClientPool()
	}
	pool[packet.IDText] = func() packet.Packet { return &testText{} }
	return pool
}
func (testProto) NewReader(r minecraft.ByteReader, shieldID int32, enableLimits bool) protocol.IO {
	return protocol.NewReader(r, shieldID, enableLimits)
}
func (testProto) NewWriter(w minecraft.ByteWriter, shieldID int32) protocol.IO {
	return protocol.NewWriter(w, shieldID)
}
func (testProto) ConvertToLatest(pk packet.Packet, _ *minecraft.Conn) []packet.Packet {
	if t, ok := pk.(*testText); ok {
		return []packet.Packet{&t.Text}
	}
	return []packet.Packet{pk}
}
func (testProto) ConvertFromLatest(pk packet.Packet, _ *minecraft.Conn) []packet.Packet {
	if t, ok := pk.(*packet.Text); ok {
		return []packet.Packet{&testText{Text: *t}}
	}
	return []packet.Packet{pk}
}

func rawOf(pk packet.Packet) []byte {
	b := bytes.NewBuffer(nil)
	_ = (&packet.Header{PacketID: pk.ID()}).Write(b)
	pk.Marshal(protocol.NewWriter(b, 0))
	return b.Bytes()
}

func decodeText(t *testing.T, raw []byte, pk packet.Packet) {
	t.Helper()
	_, payload, ok := header(raw)
	if !ok {
		t.Fatal("no header")
	}
	if err := unmarshal(pk, protocol.NewReader(bytes.NewBuffer(payload), 0, false)); err != nil {
		t.Fatal(err)
	}
}

func TestWireTranslates(t *testing.T) {
	RegisterProtocol(testProto{})
	if !Supported(9999) || !Supported(protocol.CurrentProtocol) || Supported(1) {
		t.Fatal("supported set wrong")
	}
	w := wireFor(9999)
	if w == nil || !w.changed[fromServer][packet.IDText] || w.changed[fromServer][packet.IDStartGame] {
		t.Fatalf("changed set wrong: %v", w)
	}
	orig := rawOf(&testText{Text: packet.Text{TextType: packet.TextTypeRaw, Message: "hi"}, Extra: "newer"})

	out := w.toLatest(orig, fromServer)
	if len(out) != 1 {
		t.Fatalf("got %d packets", len(out))
	}
	var native packet.Text
	decodeText(t, out[0], &native)
	if native.Message != "hi" {
		t.Fatalf("native message %q", native.Message)
	}

	// Passed on unchanged: the peer gets the original bytes, Extra and all.
	back := w.fromLatest(out[0], fromServer)
	if len(back) != 1 || !bytes.Equal(back[0], orig) {
		t.Fatal("unchanged packet did not come back as the original")
	}

	// Changed in place by the relay: translated from the native struct instead.
	changed := append([]byte(nil), out[0]...)
	changed = changed[:len(changed):len(changed)]
	back = w.fromLatest(changed, fromServer)
	var tt testText
	decodeText(t, back[0], &tt)
	if tt.Message != "hi" || tt.Extra != "" {
		t.Fatalf("translated packet %+v", tt)
	}
	inPlace := w.toLatest(orig, fromServer)[0]
	inPlace[len(inPlace)-1] ^= 0xff
	if bytes.Equal(w.fromLatest(inPlace, fromServer)[0], orig) {
		t.Fatal("a packet changed in place came back as the original")
	}

	// A packet of the same format passes as it is, both ways.
	same := rawOf(&packet.SetTime{Time: 5})
	if got := w.toLatest(same, fromServer); &got[0][0] != &same[0] {
		t.Fatal("unchanged format was copied")
	}
	if got := w.fromLatest(same, fromServer); &got[0][0] != &same[0] {
		t.Fatal("unchanged format was copied")
	}
	// The relay's own versions never see the tag.
	if _, ok := untag(out[0], protocol.CurrentProtocol); ok {
		t.Fatal("tag matched another protocol")
	}
}
