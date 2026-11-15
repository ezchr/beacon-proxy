package main

import (
	"os"
	"testing"
)

// client-side-generation takes true or false (default false); anything else is refused.
func TestClientSideGenerationOption(t *testing.T) {
	inDir(t)
	_ = os.WriteFile(tomlConfigFile, []byte(`
mode = "relay"
[servers]
kits = { address = "127.0.0.1:19181", transport = "tcp" }
survival = { address = "127.0.0.1:19170", transport = "raknet", client-side-generation = true }
flat = { address = "127.0.0.1:19171", transport = "raknet", client-side-generation = false }
try = ["kits"]
`), 0o644)
	cfg, err := loadBeaconConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"kits": false, "survival": true, "flat": false}
	for _, s := range cfg.Servers {
		if s.ClientSideGeneration != want[s.Name] {
			t.Errorf("%s: ClientSideGeneration = %v, want %v", s.Name, s.ClientSideGeneration, want[s.Name])
		}
	}

	_ = os.WriteFile(tomlConfigFile, []byte(`
mode = "relay"
[servers]
kits = { address = "127.0.0.1:19181", transport = "tcp", client-side-generation = "yes" }
try = ["kits"]
`), 0o644)
	if _, err := loadBeaconConfig(); err == nil {
		t.Fatal(`client-side-generation = "yes" accepted`)
	}
}
