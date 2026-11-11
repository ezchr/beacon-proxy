package bridge

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// A client that was on a server using sub-chunk requests (Dragonfly) keeps asking for the
// sub-chunks of the columns it had there, also after a move to a server that sends its columns
// whole (Geyser): for every such column the new server sends, the client asks again. With two
// lobbies at the same coordinates that is every column around the spawn. The relay used to answer
// all of them with air, which wiped the new server's column: the lobby was invisible. A server
// that sends whole columns can't be asked either (it kicks the client), so the relay answers from
// the columns that server sent, which it keeps for this.
//
// Only the columns around the player are kept: the server's chunk publisher centre and radius
// (NetworkChunkPublisherUpdate), or the client's view distance around the newest column before
// the server sent one. A column the client can't see any more is dropped; the client asks for a
// column only after the server sent it, and that server sends it again when the player comes back.

const (
	subPendingWait     = 3 * time.Second // how long a request waits for its column before it gets air
	subPendingColsMax  = 1024            // columns with waiting requests per player; more get air
	subPendingPerCol   = 64              // waiting requests per column; more get air
	subRequestOffsets  = 1024            // offsets read from one request; more get air
	servedColumnsSlack = 2               // columns kept beyond the radius, each side
)

// servedColumn is a column as the player's server sent it, and what its sub-chunks are once asked.
type servedColumn struct {
	raw    []byte // the LevelChunk packet; dropped once parsed
	parsed bool
	ok     bool
	subs   map[int32][]byte // by sub-chunk Y, in SubChunk packet form; only ones with blocks
	// height is the Y of the highest block of each x, z of the column (index [z][x]), or noHeight.
	height [16][16]int32
}

const noHeight = math.MinInt32

// pendingSub is a request for a sub-chunk of a column the server has not sent yet.
type pendingSub struct {
	base protocol.SubChunkPos
	off  protocol.SubChunkOffset
	dim  int32
	due  time.Time // when it gets air
}

// subWatch is the rest of the served columns' state: the server's chunk publisher area and the
// one timer that answers waiting requests with air.
type subWatch struct {
	center    [2]int32
	radius    int32 // in chunks; 0 until the server sent one
	timer     *time.Timer
	timerDue  time.Time
	timerDone func() // test hook: called after the timer ran
}

// keepColumn keeps a LevelChunk the player's server sent, and answers what waited for it. Call it
// with s.mu held.
func (s *switchSession) keepColumn(pk *packet.LevelChunk, raw []byte) {
	if _, requestMode := pk.SubChunkLimit.Value(); requestMode || pk.CacheEnabled {
		return
	}
	if s.columns == nil {
		s.columns = map[[2]int32]*servedColumn{}
	}
	col := [2]int32{pk.Position[0], pk.Position[1]}
	c := &servedColumn{raw: append([]byte(nil), raw...)}
	s.columns[col] = c
	s.pruneColumnsLocked(col, false)
	waiting := s.subPending[col]
	if len(waiting) == 0 {
		return
	}
	delete(s.subPending, col)
	pks := s.answerLocked(c, waiting)
	go s.sendClient(pks...)
}

// keptRadius is how far around the centre columns are kept, in chunks.
func (s *switchSession) keptRadius() int32 {
	r := s.subw.radius
	if r <= 0 {
		r = s.chunkRadius
	}
	return min(max(r, 4), 96) + servedColumnsSlack
}

// pruneColumnsLocked drops the kept columns outside the kept radius. newest is the column just
// kept, the centre while the server has not sent one. Without force it only runs once there are
// more columns than the radius holds. s.mu held.
func (s *switchSession) pruneColumnsLocked(newest [2]int32, force bool) {
	r := s.keptRadius()
	side := 2*r + 1
	if !force && len(s.columns) <= int(side*side) {
		return
	}
	center := newest
	if s.subw.radius > 0 {
		center = s.subw.center
	}
	for col := range s.columns {
		if col[0] < center[0]-r || col[0] > center[0]+r || col[1] < center[1]-r || col[1] > center[1]+r {
			delete(s.columns, col)
		}
	}
}

// notePublisher reads the server's NetworkChunkPublisherUpdate: the area the client loads, and
// so the columns worth keeping. s.mu held.
func (s *switchSession) notePublisher(payload []byte) {
	var pk packet.NetworkChunkPublisherUpdate
	if decodeInto(&pk, payload) != nil {
		return
	}
	s.subw.center = [2]int32{pk.Position[0] >> 4, pk.Position[2] >> 4}
	s.subw.radius = int32(min(pk.Radius>>4, 1<<20))
	if s.subw.radius == 0 {
		s.subw.radius = 1
	}
	s.pruneColumnsLocked(s.subw.center, true)
}

// forgetColumns drops the kept columns and waiting requests: the player changed server or
// dimension. Call it with s.mu held.
func (s *switchSession) forgetColumns() {
	s.columns, s.subPending = nil, nil
	s.subw.center, s.subw.radius = [2]int32{}, 0
	if s.subw.timer != nil {
		s.subw.timer.Stop()
		s.subw.timer = nil
	}
}

// serveSubChunks answers a sub-chunk request from the columns the player's server sent. A request
// for a column not sent yet waits for it, and gets air if it never comes.
func (s *switchSession) serveSubChunks(req *packet.SubChunkRequest) {
	s.mu.Lock()
	out := s.serveLocked(req, time.Now())
	s.mu.Unlock()
	if len(out) > 0 {
		s.sendClient(out...)
	}
}

// serveLocked is serveSubChunks with s.mu held: it returns the answers ready now and leaves the
// rest waiting.
func (s *switchSession) serveLocked(req *packet.SubChunkRequest, now time.Time) []packet.Packet {
	offsets := req.Offsets
	if len(offsets) == 0 {
		offsets = []protocol.SubChunkOffset{{0, 0, 0}}
	}
	s.subServed += len(offsets)
	var air []pendingSub
	if len(offsets) > subRequestOffsets {
		for _, o := range offsets[subRequestOffsets:] {
			air = append(air, pendingSub{base: req.Position, off: o, dim: req.Dimension})
		}
		offsets = offsets[:subRequestOffsets]
	}
	var out []packet.Packet
	byColumn := map[[2]int32][]pendingSub{}
	var order [][2]int32
	due := now.Add(subPendingWait)
	for _, o := range offsets {
		col := [2]int32{req.Position[0] + int32(o[0]), req.Position[2] + int32(o[2])}
		if _, ok := byColumn[col]; !ok {
			order = append(order, col)
		}
		byColumn[col] = append(byColumn[col], pendingSub{base: req.Position, off: o, dim: req.Dimension, due: due})
	}
	waits := false
	for _, col := range order {
		subs := byColumn[col]
		if c, ok := s.columns[col]; ok {
			out = append(out, s.answerLocked(c, subs)...)
			continue
		}
		if s.subPending == nil {
			s.subPending = map[[2]int32][]pendingSub{}
		}
		have, known := s.subPending[col]
		if !known && len(s.subPending) >= subPendingColsMax {
			air = append(air, subs...)
			continue
		}
		if room := subPendingPerCol - len(have); len(subs) > room {
			air = append(air, subs[max(room, 0):]...)
			subs = subs[:max(room, 0)]
		}
		if len(subs) > 0 {
			s.subPending[col] = append(have, subs...)
			waits = true
		}
	}
	if len(air) > 0 {
		out = append(out, s.answerLocked(&servedColumn{parsed: true}, air)...)
	}
	if waits {
		s.armPendingLocked(due)
	}
	return out
}

// armPendingLocked makes sure the timer runs by due at the latest. s.mu held.
func (s *switchSession) armPendingLocked(due time.Time) {
	if s.subw.timer != nil && !s.subw.timerDue.After(due) {
		return
	}
	if s.subw.timer != nil {
		s.subw.timer.Stop()
	}
	s.subw.timerDue = due
	s.subw.timer = time.AfterFunc(max(time.Until(due), 0), s.airPending)
}

// airPending answers what waited too long with air, and runs the timer again for the rest.
func (s *switchSession) airPending() {
	defer s.recoverSession("sub-chunk timer")
	s.mu.Lock()
	s.subw.timer = nil
	pks := s.expireLocked(time.Now())
	done := s.subw.timerDone
	s.mu.Unlock()
	if len(pks) > 0 {
		s.sendClient(pks...)
	}
	if done != nil {
		done()
	}
}

// expireLocked returns air for the requests due by now and arms the timer for the next. s.mu held.
func (s *switchSession) expireLocked(now time.Time) []packet.Packet {
	var pks []packet.Packet
	var next time.Time
	air := &servedColumn{parsed: true}
	for col, waiting := range s.subPending {
		if len(waiting) == 0 {
			delete(s.subPending, col)
			continue
		}
		if d := waiting[0].due; d.After(now) {
			if next.IsZero() || d.Before(next) {
				next = d
			}
			continue
		}
		delete(s.subPending, col)
		pks = append(pks, s.answerLocked(air, waiting)...)
	}
	if !next.IsZero() {
		s.armPendingLocked(next)
	}
	return pks
}

// answerLocked builds the SubChunk packets answering reqs, all for column c. s.mu held.
func (s *switchSession) answerLocked(c *servedColumn, reqs []pendingSub) []packet.Packet {
	if !c.parsed {
		c.parsed = true
		air := s.palette.index[blockState{Name: "minecraft:air", Properties: encodeProperties(nil)}]
		if err := c.parse(air); err != nil {
			s.log.Warn("network: a server's column could not be split into sub-chunks, the client gets air for it", "err", err)
		}
		c.raw = nil // the sub-chunks are all that is answered from now on
	}
	type key struct {
		base protocol.SubChunkPos
		dim  int32
	}
	byBase := map[key]*packet.SubChunk{}
	var order []*packet.SubChunk
	for _, r := range reqs {
		k := key{r.base, r.dim}
		pk, ok := byBase[k]
		if !ok {
			pk = &packet.SubChunk{Dimension: r.dim, Position: r.base}
			byBase[k] = pk
			order = append(order, pk)
		}
		pk.SubChunkEntries = append(pk.SubChunkEntries, c.entry(r.off, r.base[1]+int32(r.off[1])))
	}
	out := make([]packet.Packet, len(order))
	for i, pk := range order {
		out[i] = pk
	}
	return out
}

// entry is the answer for the sub-chunk at Y y of the column.
func (c *servedColumn) entry(off protocol.SubChunkOffset, y int32) protocol.SubChunkEntry {
	e := protocol.SubChunkEntry{Offset: off, Result: protocol.SubChunkResultSuccessAllAir}
	if data, ok := c.subs[y]; ok && c.ok {
		e.Result = protocol.SubChunkResultSuccess
		e.RawPayload = protocol.Option(data)
	}
	var hm protocol.HeightMap
	higher, lower := true, true
	for z := 0; z < 16; z++ {
		for x := 0; x < 16; x++ {
			h := c.height[z][x]
			switch {
			case !c.ok || h == noHeight || h>>4 < y:
				hm[z][x], higher = -1, false
			case h>>4 > y:
				hm[z][x], lower = 16, false
			default:
				hm[z][x], higher, lower = int8(h&15), false, false
			}
		}
	}
	switch {
	case higher:
		e.HeightMapType = protocol.HeightMapDataTooHigh
	case lower:
		e.HeightMapType = protocol.HeightMapDataTooLow
	default:
		e.HeightMapType = protocol.HeightMapDataHasData
		e.HeightMapData = protocol.Option(hm)
	}
	// The render height map as Dragonfly sends it: the same again. (With "all copied" as its type
	// the client dropped the connection.)
	e.RenderHeightMapType, e.RenderHeightMapData = e.HeightMapType, e.HeightMapData
	return e
}

// parse splits the column's LevelChunk into its sub-chunks. air is the client's id of air.
func (c *servedColumn) parse(air uint32) error {
	for z := range c.height {
		for x := range c.height[z] {
			c.height[z][x] = noHeight
		}
	}
	_, payload, err := splitPacket(c.raw)
	if err != nil {
		return err
	}
	var pk packet.LevelChunk
	if err := decodeInto(&pk, payload); err != nil {
		return err
	}
	minY := int32(-4)
	if pk.Dimension != dimOverworld {
		minY = 0
	}
	c.subs = map[int32][]byte{}
	buf := bytes.NewBuffer(pk.RawPayload)
	for i := 0; i < int(pk.SubChunkCount); i++ {
		ver, err := buf.ReadByte()
		if err != nil {
			return err
		}
		storages, y := byte(1), minY+int32(i)
		switch ver {
		case 1:
		case 8, 9:
			if storages, err = buf.ReadByte(); err != nil {
				return err
			}
			if ver == 9 {
				b, err := buf.ReadByte()
				if err != nil {
					return err
				}
				y = int32(int8(b))
			}
		default:
			return fmt.Errorf("unknown sub-chunk version %d", ver)
		}
		start := len(pk.RawPayload) - buf.Len()
		blocks := false
		for n := byte(0); n < storages; n++ {
			tops, any, err := readStorage(buf, air)
			if err != nil {
				return err
			}
			if !any {
				continue
			}
			blocks = true
			for z := 0; z < 16; z++ {
				for x := 0; x < 16; x++ {
					if t := tops[z][x]; t >= 0 {
						if h := y<<4 + int32(t); h > c.height[z][x] {
							c.height[z][x] = h
						}
					}
				}
			}
		}
		if !blocks {
			continue
		}
		end := len(pk.RawPayload) - buf.Len()
		data := make([]byte, 0, end-start+3)
		data = append(data, 9, storages, byte(int8(y)))
		c.subs[y] = append(data, pk.RawPayload[start:end]...)
	}
	c.ok = true
	return nil
}

// readStorage reads one block storage of a sub-chunk. tops is the highest block that is not air of
// each x, z (index [z][x], -1 for none) and any whether there is one at all.
func readStorage(buf *bytes.Buffer, air uint32) (tops [16][16]int8, any bool, err error) {
	for z := range tops {
		for x := range tops[z] {
			tops[z][x] = -1
		}
	}
	head, err := buf.ReadByte()
	if err != nil {
		return tops, false, err
	}
	bitsPer := int(head >> 1)
	if bitsPer == 0x7f {
		return tops, false, nil // same as the storage before it: nothing new for the heights
	}
	if bitsPer > 32 {
		return tops, false, fmt.Errorf("palette of %d bits", bitsPer)
	}
	var words []byte
	perWord := 0
	if bitsPer != 0 {
		perWord = 32 / bitsPer
		n := 4096 / perWord
		if bitsPer == 3 || bitsPer == 5 || bitsPer == 6 {
			n++
		}
		words = buf.Next(n * 4)
		if len(words) != n*4 {
			return tops, false, errors.New("short block data")
		}
	}
	count := int32(1)
	if bitsPer != 0 {
		if err := protocol.Varint32(buf, &count); err != nil {
			return tops, false, err
		}
		if count <= 0 || count > 4096 {
			return tops, false, fmt.Errorf("palette of %d entries", count)
		}
	}
	solid := make([]bool, count)
	for i := range solid {
		var v int32
		if err := protocol.Varint32(buf, &v); err != nil {
			return tops, false, err
		}
		solid[i] = uint32(v) != air
		any = any || solid[i]
	}
	if !any {
		return tops, false, nil
	}
	if bitsPer == 0 {
		for z := range tops {
			for x := range tops[z] {
				tops[z][x] = 15
			}
		}
		return tops, true, nil
	}
	mask := uint32(1)<<bitsPer - 1
	any = false
	for i := 0; i < 4096; i++ {
		w := i / perWord * 4
		word := uint32(words[w]) | uint32(words[w+1])<<8 | uint32(words[w+2])<<16 | uint32(words[w+3])<<24
		v := word >> (i % perWord * bitsPer) & mask
		if int(v) >= len(solid) || !solid[v] {
			continue
		}
		x, z, y := i>>8, i>>4&15, i&15
		if int8(y) > tops[z][x] {
			tops[z][x] = int8(y)
		}
		any = true
	}
	return tops, any, nil
}
