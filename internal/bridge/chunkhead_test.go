package bridge

import (
	"bytes"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func TestReadChunkHead(t *testing.T) {
	for _, pk := range []*packet.LevelChunk{
		{Position: protocol.ChunkPos{-5, 70000}, Dimension: 1, SubChunkCount: 24, RawPayload: []byte{1, 2, 3}},
		{Position: protocol.ChunkPos{3, -2}, SubChunkLimit: protocol.Option(int32(12)), CacheEnabled: true},
		{Position: protocol.ChunkPos{0, 0}, SubChunkLimit: protocol.Option(int32(-1))},
	} {
		id, payload, err := splitPacket(encodePacket(pk))
		if err != nil || id != packet.IDLevelChunk {
			t.Fatal(id, err)
		}
		h, ok := readChunkHead(payload)
		limit, req := pk.SubChunkLimit.Value()
		if !ok || h.pos != pk.Position || h.requestMode != req || h.cache != pk.CacheEnabled || (req && h.limit != limit) {
			t.Fatalf("%+v from %+v", h, pk)
		}
	}
}

func TestSplitPacketSubClientBits(t *testing.T) {
	// Sub-client bits above the id must not leak into it.
	b := bytes.NewBuffer(nil)
	_ = (&packet.Header{PacketID: packet.IDLevelChunk, SenderSubClient: 3, TargetSubClient: 2}).Write(b)
	b.WriteByte(9)
	id, payload, err := splitPacket(b.Bytes())
	if err != nil || id != packet.IDLevelChunk || len(payload) != 1 || payload[0] != 9 {
		t.Fatal(id, payload, err)
	}
	if _, _, err := splitPacket([]byte{0x80}); err == nil {
		t.Fatal("a cut header must fail")
	}
}
