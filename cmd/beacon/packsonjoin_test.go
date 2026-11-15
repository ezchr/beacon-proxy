package main

import (
	"os"
	"testing"
)

// resource-packs takes true, false and "on-join"; anything else is refused.
func TestResourcePacksOnJoin(t *testing.T) {
	inDir(t)
	_ = os.WriteFile(tomlConfigFile, []byte(`
mode = "relay"
[servers]
kits = { address = "127.0.0.1:19181", transport = "tcp" }
lulu = { address = "127.0.0.1:19170", transport = "raknet", resource-packs = "on-join" }
old = { address = "127.0.0.1:19171", transport = "raknet", resource-packs = false }
new = { address = "127.0.0.1:19172", transport = "raknet", resource-packs = true }
try = ["kits"]
`), 0o644)
	cfg, err := loadBeaconConfig()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]bool{}
	for _, s := range cfg.Servers {
		got[s.Name] = [2]bool{s.NoPacks, s.PacksOnJoin}
	}
	want := map[string][2]bool{"kits": {}, "lulu": {false, true}, "old": {true, false}, "new": {}}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: NoPacks, PacksOnJoin = %v, want %v", name, got[name], w)
		}
	}

	_ = os.WriteFile(tomlConfigFile, []byte(`
mode = "relay"
[servers]
kits = { address = "127.0.0.1:19181", transport = "tcp", resource-packs = "later" }
try = ["kits"]
`), 0o644)
	if _, err := loadBeaconConfig(); err == nil {
		t.Fatal(`resource-packs = "later" accepted`)
	}
}
