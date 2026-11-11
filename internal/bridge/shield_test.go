package bridge

import (
	"bytes"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// TestRewriteKeepsShieldBlockingTick is a kick seen on a live server: a MobEquipment holding a
// shield, rewritten on its way to the server, lost the shield's blocking tick, and the server read
// past the end of the packet ("decode packet *packet.MobEquipment: EOF").
func TestRewriteKeepsShieldBlockingTick(t *testing.T) {
	const clientShield, serverShield = 380, 390
	r := newRewriter(1, 1, 1, 1, nil)
	r.items.Store(&itemMap{to: map[int32]int32{serverShield: clientShield}, back: map[int32]int32{clientShield: serverShield}})
	r.shields.Store(&[2]int32{serverShield, clientShield})

	pk := &packet.MobEquipment{
		EntityRuntimeID: 1,
		NewItem: protocol.ItemInstance{Stack: protocol.ItemStack{
			ItemType: protocol.ItemType{NetworkID: clientShield}, Count: 1, BlockingTick: 7,
			NBTData: map[string]any{}, CanBePlacedOn: []string{}, CanBreak: []string{},
		}},
		InventorySlot: 1, HotBarSlot: 1,
	}
	raw := encodePacketShield(pk, clientShield)
	out, err := r.rewrite(raw, false, packet.NewClientPool())
	if err != nil {
		t.Fatal(err)
	}
	_, payload, _ := splitPacket(out)
	got := &packet.MobEquipment{}
	// The server decodes strictly with its own shield id: it must not run out of bytes.
	rd := protocol.NewReader(bytes.NewBuffer(payload), serverShield, false)
	func() {
		defer func() {
			if e := recover(); e != nil {
				t.Fatalf("server could not decode the rewritten packet: %v", e)
			}
		}()
		got.Marshal(rd)
	}()
	if got.NewItem.Stack.NetworkID != serverShield || got.NewItem.Stack.BlockingTick != 7 {
		t.Fatalf("got item %d blocking tick %d, want %d and 7", got.NewItem.Stack.NetworkID, got.NewItem.Stack.BlockingTick, serverShield)
	}
}
