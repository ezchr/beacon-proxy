package bridge

import (
	"bytes"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

const testAir, testStone = 7, 42

// testColumn is a LevelChunk of one sub-chunk (Y -4) with a stone in it.
func testColumn(x, z int32) (*packet.LevelChunk, []byte) {
	b := bytes.NewBuffer(nil)
	b.Write([]byte{8, 1, 1<<1 | 1})
	b.Write(make([]byte, 128*4))
	b.Bytes()[3] = 1 // block 0
	_ = protocol.WriteVarint32(b, 2)
	_ = protocol.WriteVarint32(b, testAir)
	_ = protocol.WriteVarint32(b, testStone)
	pk := &packet.LevelChunk{Position: protocol.ChunkPos{x, z}, SubChunkCount: 1, RawPayload: b.Bytes()}
	return pk, encodePacket(pk)
}

func testServeSession() *switchSession {
	air := blockState{Name: "minecraft:air", Properties: encodeProperties(nil)}
	return &switchSession{chunkRadius: 8, palette: &blockPalette{index: map[blockState]uint32{air: testAir}}}
}

func entries(pks []packet.Packet) (ok, air int) {
	for _, p := range pks {
		for _, e := range p.(*packet.SubChunk).SubChunkEntries {
			if e.Result == protocol.SubChunkResultSuccess {
				ok++
			} else {
				air++
			}
		}
	}
	return
}

// Walking through a big area keeps only the columns around the server's publisher centre, and a
// request right after a move is still answered from the column the new server just sent.
func TestServedColumnsKeptAroundPublisher(t *testing.T) {
	s := testServeSession()
	s.notePublisher(encodePacket(&packet.NetworkChunkPublisherUpdate{Radius: 4 << 4})[1:])
	if s.subw.radius != 4 {
		t.Fatalf("publisher radius %d, want 4", s.subw.radius)
	}
	for x := int32(-50); x < 50; x++ {
		for z := int32(-10); z < 10; z++ {
			s.keepColumn(testColumn(x, z))
		}
	}
	r := s.keptRadius()
	if max := int((2*r + 1) * (2*r + 1)); len(s.columns) > max {
		t.Fatalf("%d columns kept, want at most %d", len(s.columns), max)
	}
	for col := range s.columns {
		if col[0] < -r || col[0] > r || col[1] < -r || col[1] > r {
			t.Fatalf("column %v kept outside the radius", col)
		}
	}
	// The player moves: the publisher moves, the old area goes, the new column answers at once.
	s.notePublisher(encodePacket(&packet.NetworkChunkPublisherUpdate{Position: protocol.BlockPos{1000 << 4, 0, 0}, Radius: 4 << 4})[1:])
	if len(s.columns) != 0 {
		t.Fatalf("%d columns of the old area still kept", len(s.columns))
	}
	s.keepColumn(testColumn(1000, 0))
	out := s.serveLocked(&packet.SubChunkRequest{Position: protocol.SubChunkPos{1000, -4, 0}, Offsets: []protocol.SubChunkOffset{{0, 0, 0}}}, time.Now())
	if ok, air := entries(out); ok != 1 || air != 0 {
		t.Fatalf("answer after the move: %d with blocks, %d air", ok, air)
	}
	if c := s.columns[[2]int32{1000, 0}]; c.raw != nil {
		t.Error("raw column still kept after it was split")
	}
	// Answered again from the sub-chunks alone.
	out = s.serveLocked(&packet.SubChunkRequest{Position: protocol.SubChunkPos{1000, -4, 0}}, time.Now())
	if ok, _ := entries(out); ok != 1 {
		t.Fatal("second answer lost the blocks")
	}
}

// Before any publisher update the client's view distance around the newest column bounds it.
func TestServedColumnsBoundedWithoutPublisher(t *testing.T) {
	s := testServeSession()
	for x := int32(0); x < 3000; x++ {
		s.keepColumn(testColumn(x, x%7))
	}
	r := s.keptRadius()
	if max := int((2*r + 1) * (2*r + 1)); len(s.columns) > max {
		t.Fatalf("%d columns kept, want at most %d", len(s.columns), max)
	}
	if _, ok := s.columns[[2]int32{2999, 2999 % 7}]; !ok {
		t.Fatal("newest column dropped")
	}
}

// Requests for columns not sent are capped per player and per column; the rest is air at once,
// one timer serves all of them, and they get air when it runs.
func TestSubPendingCapped(t *testing.T) {
	s := testServeSession()
	now := time.Now()
	req := &packet.SubChunkRequest{}
	for i := 0; i < 1500; i++ {
		req.Offsets = append(req.Offsets, protocol.SubChunkOffset{int8(i % 100), 0, int8(i / 100)})
	}
	// 1500 offsets: 1024 read, of 1024 different columns (all wait), 476 air.
	out := s.serveLocked(req, now)
	if _, air := entries(out); air != 1500-subRequestOffsets {
		t.Fatalf("%d air at once, want %d", air, 1500-subRequestOffsets)
	}
	if len(s.subPending) != subPendingColsMax {
		t.Fatalf("%d columns waiting, want %d", len(s.subPending), subPendingColsMax)
	}
	// Another column: full, air at once.
	out = s.serveLocked(&packet.SubChunkRequest{Position: protocol.SubChunkPos{500, 0, 500}}, now)
	if _, air := entries(out); air != 1 || len(s.subPending) != subPendingColsMax {
		t.Fatalf("over the column cap: %d air, %d waiting", air, len(s.subPending))
	}
	// A waiting column takes at most subPendingPerCol requests.
	many := &packet.SubChunkRequest{}
	for i := 0; i < 100; i++ {
		many.Offsets = append(many.Offsets, protocol.SubChunkOffset{0, int8(i % 24), 0})
	}
	out = s.serveLocked(many, now)
	if _, air := entries(out); air != 100-(subPendingPerCol-1) || len(s.subPending[[2]int32{0, 0}]) != subPendingPerCol {
		t.Fatalf("over the column cap: %d air, %d waiting", air, len(s.subPending[[2]int32{0, 0}]))
	}
	if s.subw.timer == nil {
		t.Fatal("no timer for the waiting requests")
	}
	s.subw.timer.Stop()
	if out := s.expireLocked(now.Add(time.Second)); len(out) != 0 {
		t.Fatal("air before the wait")
	}
	out = s.expireLocked(now.Add(subPendingWait))
	if _, air := entries(out); air != subPendingColsMax-1+subPendingPerCol || len(s.subPending) != 0 {
		t.Fatalf("after the wait: %d air, %d still waiting", air, len(s.subPending))
	}
	s.forgetColumns()
	if s.subw.timer != nil {
		t.Fatal("timer kept after forgetColumns")
	}
}
