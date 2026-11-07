package bridge

import "testing"

func TestPlayerCap(t *testing.T) {
	PlayerCountDir = t.TempDir()
	defer SetPlayerCap(0)
	SetPlayerCap(2)
	if !takeSlot(false) || !takeSlot(false) {
		t.Fatal("two players fit a cap of 2")
	}
	if takeSlot(false) {
		t.Fatal("a third player must be refused")
	}
	if !takeSlot(true) {
		t.Fatal("admins always get in")
	}
	noteBedrock(-3)
	SetPlayerCap(0)
	for i := 0; i < 5; i++ {
		if !takeSlot(false) {
			t.Fatal("no cap means no limit")
		}
	}
	noteBedrock(-5)
}
