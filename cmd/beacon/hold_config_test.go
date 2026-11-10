package main

import (
	"os"
	"slices"
	"testing"
	"time"
)

func TestServerHold(t *testing.T) {
	inDir(t)
	_ = os.WriteFile(tomlConfigFile, []byte(`
mode = "relay"
[servers]
kits = { address = "127.0.0.1:19181", transport = "tcp", hold = "90s", hold-message = " Back soon ", hold-reasons = ["maintenance", ""] }
pvp = { address = "127.0.0.1:19182", transport = "tcp", hold = 45 }
lobby = "http://127.0.0.1:19133"
try = ["lobby"]
`), 0o644)
	cfg, err := loadBeaconConfig()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]time.Duration{}
	for _, s := range cfg.Servers {
		got[s.Name] = s.Hold
		if s.Name == "kits" && (s.HoldMessage != "Back soon" || !slices.Equal(s.HoldReasons, []string{"maintenance"})) {
			t.Fatalf("kits: message %q reasons %q", s.HoldMessage, s.HoldReasons)
		}
	}
	if got["kits"] != 90*time.Second || got["pvp"] != 45*time.Second || got["lobby"] != 0 {
		t.Fatalf("holds: %v", got)
	}
}

func TestServerHoldRefused(t *testing.T) {
	for _, bad := range []string{`hold = "soon"`, `hold = "-5s"`, `hold = "1h"`, `hold = "2s"`, `hold = true`, `hold-message = 5`, `hold-reasons = "restart"`} {
		inDir(t)
		_ = os.WriteFile(tomlConfigFile, []byte("mode = \"relay\"\n[servers]\nkits = { address = \"127.0.0.1:19181\", transport = \"tcp\", "+bad+" }\ntry = [\"kits\"]\n"), 0o644)
		if _, err := loadBeaconConfig(); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
}
