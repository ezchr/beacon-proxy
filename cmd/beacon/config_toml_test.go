package main

import (
	"os"
	"slices"
	"testing"

	"github.com/ezchr/beacon-proxy/internal/bridge"
)

// inDir runs the test from a fresh directory, where beacon.toml/config.json are looked up.
func inDir(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func TestDefaultBeaconTOML(t *testing.T) {
	inDir(t)
	cfg, err := loadBeaconConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tomlConfigFile); err != nil {
		t.Fatal("beacon.toml was not written:", err)
	}
	if cfg.BackendTransport != bridge.TransportNetherNet || cfg.DefaultServer != "lobby" || len(cfg.Servers) != 1 {
		t.Fatalf("default config: transport %q default %q servers %v", cfg.BackendTransport, cfg.DefaultServer, cfg.Servers)
	}
	if !cfg.RelayDirectIP || cfg.RelayDirectIPListenAddress != "0.0.0.0:19132" {
		t.Fatalf("default bind not used: %v %q", cfg.RelayDirectIP, cfg.RelayDirectIPListenAddress)
	}
}

func TestRelayServers(t *testing.T) {
	inDir(t)
	_ = os.WriteFile(tomlConfigFile, []byte(`
mode = "relay"
motd = "Test \"World\""
[servers]
skygen = { address = "127.0.0.1:19142", transport = "raknet" }
pvp = "http://127.0.0.1:19133"
try = ["pvp"]
[broadcast]
accept-friends = false
`), 0o644)
	cfg, err := loadBeaconConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorldName != `Test "World"` || cfg.HostName != cfg.WorldName || !cfg.NoFriendAccept {
		t.Fatalf("names/friends: %q %q %v", cfg.WorldName, cfg.HostName, cfg.NoFriendAccept)
	}
	names := []string{cfg.Servers[0].Name, cfg.Servers[1].Name}
	if !slices.Equal(names, []string{"pvp", "skygen"}) || cfg.Servers[1].Transport != bridge.TransportRakNet || cfg.Servers[0].Transport != "" {
		t.Fatalf("servers: %+v", cfg.Servers)
	}
}

func TestRelaylessRejectsRakNet(t *testing.T) {
	inDir(t)
	_ = os.WriteFile(tomlConfigFile, []byte(`
mode = "relayless"
[servers]
main = { address = "127.0.0.1:19132", transport = "raknet" }
`), 0o644)
	if _, err := loadBeaconConfig(); err == nil {
		t.Fatal("relayless mode took a RakNet server")
	}
}

func TestMigrateLegacyConfig(t *testing.T) {
	for _, tc := range []struct {
		name, json, transport string
		servers               int
	}{
		{"network", `{"backend_transport":"nethernet","world_name":"W","host_name":"H","admins":["a"],
			"servers":[{"name":"pvp","address":"http://127.0.0.1:19133"},{"name":"sky","address":"http://127.0.0.1:19142"}],
			"default_server":"sky","relay_direct_ip":true,"relay_direct_ip_listen_address":"127.0.0.1:19136",
			"fake_player_count":21,"fake_player_drift":{"min":16,"max":26,"max_step":2}}`, bridge.TransportNetherNet, 2},
		{"norelay", `{"backend_transport":"nethernet-norelay","server_address":"127.0.0.1:19134","world_name":"W"}`,
			bridge.TransportNetherNetNoRelay, 0},
		{"raknet", `{"backend_transport":"raknet","server_address":"127.0.0.1:19132"}`, bridge.TransportNetherNet, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inDir(t)
			_ = os.WriteFile(legacyConfigFile, []byte(tc.json), 0o644)
			cfg, err := loadBeaconConfig()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(legacyConfigFile + ".migrated"); err != nil {
				t.Fatal("config.json was not kept as config.json.migrated")
			}
			if cfg.BackendTransport != tc.transport || len(cfg.Servers) != tc.servers {
				t.Fatalf("transport %q servers %+v", cfg.BackendTransport, cfg.Servers)
			}
			switch tc.name {
			case "network":
				if cfg.DefaultServer != "sky" || cfg.HostName != "H" || cfg.FakePlayerDrift == nil || cfg.FakePlayerDrift.Max != 26 || cfg.RelayDirectIPListenAddress != "127.0.0.1:19136" {
					t.Fatalf("network config lost settings: %+v", cfg)
				}
			case "norelay":
				if cfg.NetherNetBackendAddress != "http://127.0.0.1:19134" {
					t.Fatalf("norelay address %q", cfg.NetherNetBackendAddress)
				}
			case "raknet":
				if cfg.Servers[0].Transport != bridge.TransportRakNet {
					t.Fatalf("raknet server lost its transport: %+v", cfg.Servers)
				}
			}
		})
	}
}

func TestNewRelaySettings(t *testing.T) {
	inDir(t)
	_ = os.WriteFile(tomlConfigFile, []byte(`
ping-passthrough = "players"
[servers]
lobby = "127.0.0.1:19133"
pvp = "127.0.0.1:19134"
try = ["lobby", "pvp"]
[forced-hosts]
"pvp.example.com" = ["pvp"]
[advanced]
login-ratelimit = 3000
packets-per-second = 500
`), 0o644)
	cfg, err := loadBeaconConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Try, []string{"lobby", "pvp"}) || !slices.Equal(cfg.ForcedHosts["pvp.example.com"], []string{"pvp"}) {
		t.Fatalf("try %v forced %v", cfg.Try, cfg.ForcedHosts)
	}
	if cfg.PingPassthrough != "players" || cfg.LoginRateLimit != 3000 || cfg.PacketsPerSecond != 500 {
		t.Fatalf("passthrough %q limits %d %d", cfg.PingPassthrough, cfg.LoginRateLimit, cfg.PacketsPerSecond)
	}
	_ = os.WriteFile(tomlConfigFile, []byte("ping-passthrough = \"sometimes\"\n[servers]\nlobby = \"127.0.0.1:1\"\n"), 0o644)
	if _, err := loadBeaconConfig(); err == nil {
		t.Fatal("bad ping-passthrough accepted")
	}
}
