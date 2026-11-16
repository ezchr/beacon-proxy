package bridge

import (
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// A server numbering blocks by network hashes: block sounds and break particles name the block by
// its whole 32-bit hash, which is negative as an int32 half the time.
func TestHashedBlockSoundAndParticles(t *testing.T) {
	var hash = uint32(0xF1234567) // negative as int32
	r := &rewriter{blocks: &blockMap{hashTo: map[uint32]uint32{hash: 42}, back: []uint32{}}}

	snd := &packet.LevelSoundEvent{SoundType: packet.SoundEventPlace, ExtraData: int32(hash)}
	if !r.special(snd, true) || snd.ExtraData != 42 {
		t.Fatalf("place sound: got %d, want 42", snd.ExtraData)
	}
	none := &packet.LevelSoundEvent{SoundType: packet.SoundEventPlace, ExtraData: -1}
	r.special(none, true)
	if none.ExtraData != -1 {
		t.Fatalf("no-block sound changed to %d", none.ExtraData)
	}
	ev := &packet.LevelEvent{EventType: packet.LevelEventParticlesDestroyBlock, EventData: int32(hash)}
	if !r.special(ev, true) || ev.EventData != 42 {
		t.Fatalf("break particles: got %d, want 42", ev.EventData)
	}
}
