package bridge

import (
	"fmt"
	"math"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// chunkSettle sends the chunks around the player's spawn again a little after a move, a dimension
// change or a respawn. A client still finishing its spawn drops chunks, and the first ones a
// server sends are the ones at the spawn: Geyser sends the real spawn chunk right after its
// Respawn packet, about when the client confirms the dimension change, and the client kept the
// empty chunk Geyser sent before it - an invisible, solid hole at the spawn (chunk 0,0 of the
// Skygen lobby, after joining or dying). The newest copy of each chunk near the spawn is kept for
// a few seconds and sent again once the client has settled.
type chunkSettle struct {
	l      *link
	center [2]int32
	until  time.Time
	gen    int
	chunks map[[2]int32]*keptChunk
	// far holds the newest chunk of every column further out, for wiped columns.
	far      map[[2]int32]*keptChunk
	farUntil time.Time
	// wiped are the columns the relay answered a sub-chunk request for with air while the server
	// sends its columns whole; the server's column is sent again shortly after.
	wiped        map[[2]int32]bool
	wipedPending bool
	wipedTotal   int
	wipedReports int
}

// keptChunk is a chunk and the block changes the server sent for it since, which are sent again
// after it so that the older chunk doesn't undo them.
type keptChunk struct {
	chunk []byte
	after [][]byte
}

const (
	settleRadius = 3                // chunks around the spawn kept, in every direction
	settleKeep   = 4 * time.Second  // how long after arming chunks are kept
	farRadius    = 16               // chunks around the spawn kept for wiped columns
	farKeep      = 30 * time.Second // how long after arming those are kept
	wipedDelay   = 300 * time.Millisecond
	// maxAfter caps the block changes kept per chunk. They only matter inside the settle window.
	maxAfter = 512
)

// settleResends are when, after arming, the kept chunks are sent again.
var settleResends = []time.Duration{time.Second, 2500 * time.Millisecond}

// armSettle starts keeping the chunks around pos that l sends. Call it with s.mu held.
func (s *switchSession) armSettle(l *link, pos mgl32.Vec3) {
	c := [2]int32{int32(math.Floor(float64(pos[0]))) >> 4, int32(math.Floor(float64(pos[2]))) >> 4}
	st := &s.settle
	// Start over at every arm. Keeping the old maps when a player respawned at the same spawn chunk
	// (every death on a one-spawn server) kept every later block change around that spawn for the
	// whole connection: gigabytes on a busy PvP arena next to the spawn.
	if st.l != l || st.center != c {
		st.wipedReports = 0
	}
	st.chunks = map[[2]int32]*keptChunk{}
	st.far = map[[2]int32]*keptChunk{}
	st.l, st.center, st.until = l, c, time.Now().Add(settleKeep)
	st.farUntil = time.Now().Add(farKeep)
	st.gen++
	gen := st.gen
	for _, d := range settleResends {
		time.AfterFunc(d, func() { s.resendSettled(l, gen) })
	}
}

// observeSettle looks at a packet l sends the client: chunks near an armed spawn are kept, and
// a dimension change or a respawn arms one. Call it with s.mu held.
func (s *switchSession) observeSettle(l *link, raw []byte) {
	id, payload, err := splitPacket(raw)
	if err != nil {
		return
	}
	switch id {
	case packet.IDChangeDimension:
		var pk packet.ChangeDimension
		if decodeInto(&pk, payload) == nil {
			s.armSettle(l, pk.Position)
		}
	case packet.IDRespawn:
		var pk packet.Respawn
		if decodeInto(&pk, payload) == nil && pk.State == packet.RespawnStateReadyToSpawn {
			s.armSettle(l, pk.Position)
		}
	case packet.IDUpdateBlock, packet.IDUpdateSubChunkBlocks, packet.IDBlockActorData, packet.IDLevelChunk:
		if s.settle.expire() {
			return
		}
	}
	switch id {
	case packet.IDUpdateBlock:
		var pk packet.UpdateBlock
		if decodeInto(&pk, payload) == nil {
			s.settle.changed(pk.Position, raw)
		}
	case packet.IDUpdateSubChunkBlocks:
		var pk packet.UpdateSubChunkBlocks
		if decodeInto(&pk, payload) == nil {
			for _, e := range append(pk.Blocks, pk.Extra...) {
				s.settle.changed(e.BlockPos, raw)
			}
		}
	case packet.IDBlockActorData:
		var pk packet.BlockActorData
		if decodeInto(&pk, payload) == nil {
			s.settle.changed(pk.Position, raw)
		}
	case packet.IDLevelChunk:
		st := &s.settle
		if st.l != l || st.chunks == nil || time.Now().After(st.farUntil) {
			return
		}
		h, ok := readChunkHead(payload)
		if !ok {
			return
		}
		pk := struct{ Position protocol.ChunkPos }{h.pos}
		dx, dz := pk.Position[0]-st.center[0], pk.Position[1]-st.center[1]
		if dx < -farRadius || dx > farRadius || dz < -farRadius || dz > farRadius {
			return
		}
		col := [2]int32{pk.Position[0], pk.Position[1]}
		st.far[col] = &keptChunk{chunk: append([]byte(nil), raw...)}
		if time.Now().After(st.until) || dx < -settleRadius || dx > settleRadius || dz < -settleRadius || dz > settleRadius {
			return
		}
		st.chunks[col] = st.far[col]
	}
}

// expire drops the kept chunks once the far window is over and reports whether nothing is kept
// (then there is nothing to record either).
func (st *chunkSettle) expire() bool {
	if st.chunks == nil && st.far == nil {
		return true
	}
	if time.Now().After(st.farUntil) {
		st.chunks, st.far = nil, nil
		return true
	}
	return false
}

// changed keeps a block change raw the server sent for a kept chunk, to send again after it. Only
// inside the settle window: the kept chunks are sent again only then.
func (st *chunkSettle) changed(pos protocol.BlockPos, raw []byte) {
	if time.Now().After(st.until) {
		return
	}
	if k, ok := st.chunks[[2]int32{pos[0] >> 4, pos[2] >> 4}]; ok && len(k.after) < maxAfter {
		for _, a := range k.after {
			if string(a) == string(raw) {
				return // one packet can change several blocks of the chunk
			}
		}
		k.after = append(k.after, append([]byte(nil), raw...))
	}
}

// resendSettled sends the kept chunks again, if the player is still on l, not moving, and nothing
// armed again since (that arming has its own resends).
func (s *switchSession) resendSettled(l *link, gen int) {
	defer s.recoverSession("resendSettled")
	s.mu.Lock()
	st := &s.settle
	if st.gen != gen || st.l != l || s.cur != l || s.transfer != nil || len(st.chunks) == 0 {
		s.mu.Unlock()
		return
	}
	pks := make([][]byte, 0, len(st.chunks))
	for _, k := range st.chunks {
		pks = append(pks, k.chunk)
		pks = append(pks, k.after...)
	}
	s.mu.Unlock()
	_ = s.writeClient(pks...)
	s.log.Debug("network: chunks around the spawn sent again", "chunks", len(pks))
}

// noteWiped records the columns of a sub-chunk request the relay just answered with air on l, a
// server that sends its columns whole, and has them sent again shortly.
func (s *switchSession) noteWiped(l *link, req *packet.SubChunkRequest) {
	offsets := req.Offsets
	if len(offsets) == 0 {
		offsets = []protocol.SubChunkOffset{{0, 0, 0}}
	}
	s.mu.Lock()
	st := &s.settle
	if st.wiped == nil {
		st.wiped = map[[2]int32]bool{}
	}
	for _, o := range offsets {
		st.wiped[[2]int32{req.Position[0] + int32(o[0]), req.Position[2] + int32(o[2])}] = true
	}
	st.wipedTotal += len(offsets)
	pending := st.wipedPending
	st.wipedPending = true
	s.mu.Unlock()
	if !pending {
		time.AfterFunc(wipedDelay, func() { s.resendWiped(l) })
	}
}

// resendWiped only reports the wiped columns for now (diagnosis): sending the server's chunk again
// made the client ask for the same sub-chunks again, in a loop.
func (s *switchSession) resendWiped(l *link) {
	defer s.recoverSession("resendWiped")
	s.mu.Lock()
	st := &s.settle
	st.wipedPending = false
	wiped, total := st.wiped, st.wipedTotal
	st.wiped, st.wipedTotal = nil, 0
	st.wipedReports++
	n := st.wipedReports
	var cols []string
	for col := range wiped {
		d := "no chunk seen from the server"
		if k, ok := st.far[col]; ok {
			d = describeChunk(k.chunk)
		}
		cols = append(cols, fmt.Sprintf("%d,%d: %s", col[0], col[1], d))
	}
	moving := s.transfer != nil
	center := st.center
	s.mu.Unlock()
	if n > 6 {
		return // enough to see the pattern
	}
	s.log.Info("network: sub-chunk requests answered with air on a server that sends whole columns", "server", l.server.Name, "requests", total, "spawnColumn", center, "columns", cols, "midMove", moving)
}

// describeChunk summarises a LevelChunk packet for the log.
func describeChunk(raw []byte) string {
	_, payload, err := splitPacket(raw)
	if err != nil {
		return "undecodable"
	}
	var pk packet.LevelChunk
	if decodeInto(&pk, payload) != nil {
		return "undecodable"
	}
	limit, request := pk.SubChunkLimit.Value()
	return fmt.Sprintf("subChunks=%d requestMode=%v limit=%d cache=%v bytes=%d dim=%d", pk.SubChunkCount, request, limit, pk.CacheEnabled, len(pk.RawPayload), pk.Dimension)
}
