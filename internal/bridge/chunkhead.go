package bridge

import (
	"encoding/binary"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// chunkHead is the start of a LevelChunk payload: what the relay looks at in most chunks, read
// without decoding (and copying) the blocks behind it.
type chunkHead struct {
	pos         protocol.ChunkPos
	requestMode bool  // sub-chunk request mode: the blocks come in SubChunk packets
	limit       int32 // the request mode's limit, if it has one
	hasLimit    bool
	cache       bool
}

// readChunkHead reads a LevelChunk payload's head: position, dimension, sub-chunk count, the
// optional limit and the cache flag (packet.LevelChunk.Marshal).
func readChunkHead(p []byte) (h chunkHead, ok bool) {
	x, n := binary.Varint(p) // varint32 is zigzag, as binary.Varint
	if n <= 0 {
		return h, false
	}
	p = p[n:]
	z, n := binary.Varint(p)
	if n <= 0 {
		return h, false
	}
	p = p[n:]
	if _, n = binary.Varint(p); n <= 0 { // dimension
		return h, false
	}
	p = p[n:]
	if _, n = binary.Uvarint(p); n <= 0 { // sub-chunk count
		return h, false
	}
	p = p[n:]
	if len(p) < 1 {
		return h, false
	}
	h.requestMode = p[0] != 0
	p = p[1:]
	if h.requestMode {
		l, n := binary.Varint(p)
		if n <= 0 {
			return h, false
		}
		h.limit, h.hasLimit = int32(l), true
		p = p[n:]
	}
	if len(p) < 1 {
		return h, false
	}
	h.cache = p[0] != 0
	h.pos = protocol.ChunkPos{int32(x), int32(z)}
	return h, true
}
