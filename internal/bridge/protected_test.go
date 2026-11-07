package bridge

import "testing"

func TestProtected(t *testing.T) {
	defer SetProtected(nil)
	SetProtected([]string{"RiftBot*", " tankbot? "})
	for name, want := range map[string]bool{
		"RiftBot1": true, "riftbot12": true, "java:RiftBot3": true, "TankBot7": true,
		"TankBot12": false, "Rift": false, "SomePlayer": false, "": false,
	} {
		if isProtected(name) != want {
			t.Errorf("isProtected(%q) = %v", name, !want)
		}
	}
}
