package main

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ezchr/beacon-proxy/internal/bridge"
)

// running is the config Beacon runs with: the one it started with, plus what reload changed.
var (
	runningMu sync.Mutex
	running   FileConfig
)

// applyLive sets what can change while Beacon runs.
func applyLive(cfg FileConfig) {
	bridge.SetLimits(bridge.Limits{
		LoginInterval:    time.Duration(cfg.LoginRateLimit) * time.Millisecond,
		PacketsPerSecond: cfg.PacketsPerSecond,
		SessionsPerIP:    cfg.SessionsPerIP,
	})
	bridge.SetPingPassthrough(cfg.PingPassthrough)
	chatEnabled = cfg.CrossServerChat
}

// reloadConfig rereads beacon.toml (console reload, /beacon reload), applies what can change while
// players are on, and says what needs a restart.
func reloadConfig() []string {
	runningMu.Lock()
	defer runningMu.Unlock()
	next, err := loadBeaconConfig()
	if err != nil {
		return []string{"beacon.toml not reloaded: " + err.Error()}
	}
	cur := running
	if next.BackendTransport != cur.BackendTransport {
		return []string{"mode changed: restart Beacon for that; nothing was reloaded"}
	}
	var out []string
	if network != nil {
		servers, err := normalizeServers(next.Servers)
		if err == nil {
			err = network.Reload(servers, next.Try, next.ForcedHosts, adminSet(next.Admins))
		}
		if err != nil {
			return []string{"servers not reloaded: " + err.Error()}
		}
		out = append(out, fmt.Sprintf("servers: %s, players join %s first", strings.Join(network.ServerNames(), ", "), network.DefaultServer()))
	}
	applyLive(next)
	out = append(out, "admins, forced hosts, ping passthrough, rate limits and cross-server chat reloaded")

	var restart []string
	same := func(name string, equal bool) {
		if !equal {
			restart = append(restart, name)
		}
	}
	same("bind", next.RelayDirectIPListenAddress == cur.RelayDirectIPListenAddress && next.RelayDirectIP == cur.RelayDirectIP)
	same("domain/tls", next.TLSDomain == cur.TLSDomain && next.TLSCert == cur.TLSCert && next.TLSKey == cur.TLSKey)
	same("motd", next.WorldName == cur.WorldName)
	same("show-max-players", next.MaxPlayers == cur.MaxPlayers)
	same("host-name", next.HostName == cur.HostName)
	same("control-port", next.ControlPort == cur.ControlPort)
	same("listing-toggle", next.ListingToggle == cur.ListingToggle)
	same("allowed-xuids", slices.Equal(next.AllowedXUIDs, cur.AllowedXUIDs))
	same("hosting accounts", len(next.ExtraBroadcasts) == len(cur.ExtraBroadcasts))
	if len(restart) > 0 {
		out = append(out, "changed, needs a restart: "+strings.Join(restart, ", "))
	}
	// What is reloaded is now what runs; the rest keeps its old value until a restart.
	cur.Servers, cur.Try, cur.ForcedHosts, cur.Admins = next.Servers, next.Try, next.ForcedHosts, next.Admins
	cur.PingPassthrough, cur.LoginRateLimit, cur.PacketsPerSecond = next.PingPassthrough, next.LoginRateLimit, next.PacketsPerSecond
	cur.SessionsPerIP = next.SessionsPerIP
	cur.CrossServerChat = next.CrossServerChat
	running = cur
	return out
}

func adminSet(admins []string) map[string]bool {
	m := map[string]bool{}
	for _, a := range admins {
		m[strings.ToLower(strings.TrimSpace(a))] = true
	}
	return m
}
