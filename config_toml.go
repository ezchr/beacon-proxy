package main

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"text/template"

	"github.com/ezchr/beacon-proxy/bridge"
	"github.com/pelletier/go-toml/v2"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// beacon.toml is the config file, laid out like Velocity's velocity.toml. It is read into a
// FileConfig (config.go), which the rest of the program uses as before. A config.json from before
// the rename is converted to beacon.toml on first start and renamed config.json.migrated.

const (
	tomlConfigFile    = "beacon.toml"
	legacyConfigFile  = "config.json"
	tomlConfigVersion = "1.0"

	modeRelay     = "relay"
	modeRelayless = "relayless"
)

// configCreated is set when loadBeaconConfig wrote a new default beacon.toml.
var configCreated bool

//go:embed default-beacon.toml
var defaultTOMLTemplate string

// tomlConfig mirrors beacon.toml.
type tomlConfig struct {
	ConfigVersion        string `toml:"config-version"`
	Mode                 string `toml:"mode"`
	Bind                 string `toml:"bind"`
	Domain               string `toml:"domain"`
	TLSCert              string `toml:"tls-cert"`
	TLSKey               string `toml:"tls-key"`
	MOTD                 string `toml:"motd"`
	ShowMaxPlayers       int    `toml:"show-max-players"`
	ForwardingSecretFile string `toml:"forwarding-secret-file"`

	// Servers holds "name = address" pairs, "name = { address = ..., transport = ... }" tables and
	// the "try" list, like Velocity's [servers].
	Servers map[string]any `toml:"servers"`

	Broadcast struct {
		HostName       string   `toml:"host-name"`
		TokenFile      string   `toml:"token-file"`
		Admins         []string `toml:"admins"`
		AcceptFriends  *bool    `toml:"accept-friends"`
		ListingToggle  bool     `toml:"listing-toggle"`
		AllowedXUIDs   []string `toml:"allowed-xuids"`
		UpdateInterval int      `toml:"update-interval-seconds"`
		Extra          []struct {
			Name          string `toml:"name"`
			TokenFile     string `toml:"token-file"`
			HostName      string `toml:"host-name"`
			WorldName     string `toml:"world-name"`
			Invites       bool   `toml:"invites"`
			AcceptFriends *bool  `toml:"accept-friends"`
			Protocol      int32  `toml:"protocol"`
			Version       string `toml:"version"`
		} `toml:"extra"`
	} `toml:"broadcast"`

	Advanced struct {
		ControlPort             int              `toml:"control-port"`
		Compression             string           `toml:"compression"`
		CompressionThreshold    int              `toml:"compression-threshold"`
		DisableClientEncryption bool             `toml:"disable-client-encryption"`
		Protocol                int32            `toml:"protocol"`
		Version                 string           `toml:"version"`
		NetworkDir              string           `toml:"network-dir"`
		RejoinAddress           string           `toml:"rejoin-address"`
		JavaProxy               string           `toml:"java-proxy"`
		JavaDataDir             string           `toml:"java-data-dir"`
		JavaAuth                *javaAuthOptions `toml:"java-auth"`
		CrossServerChat         bool             `toml:"cross-server-chat"`
		ACMEHTTPAddress         string           `toml:"acme-http-address"`
		FakePlayerCount         int              `toml:"fake-player-count"`
		FakePlayerDrift         *struct {
			Min     int `toml:"min"`
			Max     int `toml:"max"`
			MaxStep int `toml:"max-step"`
		} `toml:"fake-player-drift"`
	} `toml:"advanced"`
}

// loadBeaconConfig loads beacon.toml, converting a config.json first if that is all there is, or
// writing the default beacon.toml if there is neither.
func loadBeaconConfig() (FileConfig, error) {
	if _, err := os.Stat(tomlConfigFile); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(legacyConfigFile); err == nil {
			if err := migrateLegacyConfig(); err != nil {
				return FileConfig{}, fmt.Errorf("converting %s to %s: %w", legacyConfigFile, tomlConfigFile, err)
			}
		} else {
			if err := os.WriteFile(tomlConfigFile, []byte(renderTOML(defaultTemplateValues())), 0o644); err != nil {
				return FileConfig{}, err
			}
			configCreated = true
		}
	}
	b, err := os.ReadFile(tomlConfigFile)
	if err != nil {
		return FileConfig{}, err
	}
	var t tomlConfig
	if err := toml.Unmarshal(b, &t); err != nil {
		return FileConfig{}, fmt.Errorf("%s: %w", tomlConfigFile, err)
	}
	return t.fileConfig()
}

// fileConfig translates beacon.toml into the FileConfig the program runs on.
func (t tomlConfig) fileConfig() (FileConfig, error) {
	cfg := defaultConfig()
	cfg.Protocol, cfg.Version = protocol.CurrentProtocol, protocol.CurrentVersion
	if t.Advanced.Protocol != 0 {
		cfg.Protocol, cfg.Version = t.Advanced.Protocol, t.Advanced.Version
	}
	if t.MOTD != "" {
		cfg.WorldName = t.MOTD
	}
	cfg.HostName = cfg.WorldName
	if t.Broadcast.HostName != "" {
		cfg.HostName = t.Broadcast.HostName
	}
	if t.ShowMaxPlayers > 0 {
		cfg.MaxPlayers = t.ShowMaxPlayers
	}
	cfg.Admins = t.Broadcast.Admins
	cfg.NoFriendAccept = t.Broadcast.AcceptFriends != nil && !*t.Broadcast.AcceptFriends
	cfg.ListingToggle = t.Broadcast.ListingToggle
	cfg.AllowedXUIDs = t.Broadcast.AllowedXUIDs
	if t.Broadcast.UpdateInterval > 0 {
		cfg.UpdateIntervalSeconds = t.Broadcast.UpdateInterval
	}
	for i, e := range t.Broadcast.Extra {
		name := e.Name
		if name == "" {
			name = fmt.Sprintf("extra-%d", i+1)
		}
		cfg.ExtraBroadcasts = append(cfg.ExtraBroadcasts, BroadcastConfig{
			Name: name, TokenFile: e.TokenFile, HostName: e.HostName, WorldName: e.WorldName,
			Invites: e.Invites, NoFriendAccept: e.AcceptFriends != nil && !*e.AcceptFriends,
			Protocol: e.Protocol, Version: e.Version,
		})
	}

	a := t.Advanced
	cfg.ControlPort = 7777
	if a.ControlPort > 0 {
		cfg.ControlPort = a.ControlPort
	}
	if a.Compression != "" {
		cfg.Compression = a.Compression
	}
	if a.CompressionThreshold > 0 {
		cfg.CompressionThreshold = a.CompressionThreshold
	}
	cfg.DisableClientEncryption = a.DisableClientEncryption
	cfg.NetworkDir, cfg.RejoinAddress = a.NetworkDir, a.RejoinAddress
	cfg.JavaProxy, cfg.JavaDataDir, cfg.JavaAuth = a.JavaProxy, a.JavaDataDir, a.JavaAuth
	cfg.CrossServerChat = a.CrossServerChat
	cfg.ACMEHTTPAddress = a.ACMEHTTPAddress
	cfg.FakePlayerCount = a.FakePlayerCount
	if d := a.FakePlayerDrift; d != nil {
		cfg.FakePlayerDrift = &PlayerDriftConfig{Min: d.Min, Max: d.Max, MaxStep: d.MaxStep}
	}

	mode := strings.ToLower(strings.TrimSpace(t.Mode))
	servers, try, err := t.parseServers()
	if err != nil {
		return FileConfig{}, err
	}
	if len(servers) == 0 {
		return FileConfig{}, fmt.Errorf("%s has no servers: add one under [servers], e.g. lobby = \"127.0.0.1:19133\"", tomlConfigFile)
	}
	def := servers[0].Name
	if len(try) > 0 {
		def = try[0]
	}

	switch mode {
	case "", modeRelay:
		cfg.BackendTransport = bridge.TransportNetherNet
		cfg.Servers, cfg.DefaultServer = servers, def
		if t.Bind != "" {
			cfg.RelayDirectIP, cfg.RelayDirectIPListenAddress = true, t.Bind
			// A bind on this machine only (something like nginx faces the players) is private, so the
			// control API shares it. A public bind never carries it: it keeps its own local port.
			cfg.ControlOnDirectIP = isLoopback(t.Bind)
			cfg.TLSDomain, cfg.TLSCert, cfg.TLSKey = t.Domain, t.TLSCert, t.TLSKey
		} else if t.Domain != "" || t.TLSCert != "" {
			cfg.Notes = append(cfg.Notes, "domain and tls-cert are only used with bind (joining by address)")
		}
		// Every login Beacon sends a server is signed (docs/forwarding.md).
		if cfg.ForwardingSecret, err = forwardingSecret(t.ForwardingSecretFile); err != nil {
			return FileConfig{}, err
		}
		for _, s := range servers {
			if !isLoopback(s.Address) {
				cfg.Notes = append(cfg.Notes, fmt.Sprintf("WARNING: server %s is on another machine: let only Beacon reach %s (firewall), "+
					"since the server takes logins without its own Xbox check, and have it check Beacon's signature (docs/forwarding.md)", s.Name, s.Address))
			}
		}
	case modeRelayless:
		// Players connect straight to the backend, so there is one server and nothing to forward.
		s := serverNamed(servers, def)
		if strings.EqualFold(s.Transport, bridge.TransportRakNet) {
			return FileConfig{}, fmt.Errorf("server %s: relayless mode needs a NetherNet backend (its players connect to it over NetherNet); this one is set to RakNet", s.Name)
		}
		cfg.BackendTransport = bridge.TransportNetherNetNoRelay
		cfg.ServerAddress = s.Address
		if len(servers) > 1 {
			cfg.Notes = append(cfg.Notes, fmt.Sprintf("relayless mode uses one server (%s); the others need mode = \"relay\"", s.Name))
		}
		if t.Bind != "" || t.Domain != "" || t.TLSCert != "" {
			cfg.Notes = append(cfg.Notes, "bind, domain and tls-cert are not used in relayless mode: direct-IP players connect to the server itself")
		}
	default:
		return FileConfig{}, fmt.Errorf("mode %q: use \"relay\" or \"relayless\"", t.Mode)
	}
	resolveServerAddress(&cfg, nil)
	return cfg, nil
}

// parseServers reads [servers]: plain addresses, tables and the try list. Servers come out in try
// order first, then the rest by name, so the order does not depend on map iteration. A server is
// NetherNet unless it says transport = "raknet".
func (t tomlConfig) parseServers() ([]bridge.ServerConfig, []string, error) {
	var try []string
	byName := map[string]bridge.ServerConfig{}
	for name, v := range t.Servers {
		if name == "try" {
			list, ok := v.([]any)
			if !ok {
				return nil, nil, errors.New(`[servers] try must be a list, e.g. try = ["lobby"]`)
			}
			for _, x := range list {
				s, _ := x.(string)
				try = append(try, s)
			}
			continue
		}
		s := bridge.ServerConfig{Name: name}
		switch v := v.(type) {
		case string:
			s.Address = v
		case map[string]any:
			s.Address, _ = v["address"].(string)
			s.Transport, _ = v["transport"].(string)
			s.Protocols = int32List(v["protocols"])
			s.Native = int32List(v["native"])
		default:
			return nil, nil, fmt.Errorf(`server %s: give an address ("host:port") or a table with address = "host:port"`, name)
		}
		if s.Address == "" {
			return nil, nil, fmt.Errorf("server %s has no address", name)
		}
		switch strings.ToLower(s.Transport) {
		case "", bridge.TransportNetherNet:
			s.Transport = ""
		case bridge.TransportRakNet:
			s.Transport = bridge.TransportRakNet
		default:
			return nil, nil, fmt.Errorf("server %s: transport %q: use \"nethernet\" (default) or \"raknet\"", name, s.Transport)
		}
		byName[name] = s
	}
	var out []bridge.ServerConfig
	for _, name := range try {
		s, ok := byName[name]
		if !ok {
			return nil, nil, fmt.Errorf("[servers] try names %q, which is not a server", name)
		}
		out = append(out, s)
		delete(byName, name)
	}
	rest := make([]string, 0, len(byName))
	for name := range byName {
		rest = append(rest, name)
	}
	slices.Sort(rest)
	for _, name := range rest {
		out = append(out, byName[name])
	}
	return out, try, nil
}

// int32List reads a TOML list of numbers, such as protocols = [2193, 2225].
func int32List(v any) []int32 {
	list, _ := v.([]any)
	var out []int32
	for _, x := range list {
		if n, ok := x.(int64); ok {
			out = append(out, int32(n))
		}
	}
	return out
}

func serverNamed(servers []bridge.ServerConfig, name string) bridge.ServerConfig {
	for _, s := range servers {
		if s.Name == name {
			return s
		}
	}
	return servers[0]
}

// forwardingSecret reads the forwarding secret: BEACON_FORWARDING_SECRET if set, else the file,
// which is created with a random secret if it does not exist yet.
func forwardingSecret(file string) ([]byte, error) {
	if v := strings.TrimSpace(os.Getenv("BEACON_FORWARDING_SECRET")); v != "" {
		return []byte(v), nil
	}
	if file == "" {
		file = "forwarding.secret"
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		b = []byte(base64.RawURLEncoding.EncodeToString(raw))
		if err := os.WriteFile(file, append(b, '\n'), 0o600); err != nil {
			return nil, err
		}
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if b = bytes.TrimSpace(b); len(b) == 0 {
		return nil, fmt.Errorf("%s is empty: delete it and Beacon makes a new secret", file)
	}
	return b, nil
}

// isLoopback reports whether a server address is on this machine.
func isLoopback(addr string) bool {
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://"), "/")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// templateValues fills default-beacon.toml.
type templateValues struct {
	Mode, Bind, MOTD, HostName string
	ShowMaxPlayers             int
	Servers                    []bridge.ServerConfig
	Try                        []string
	Admins                     []string
	AcceptFriends              bool
	ListingToggle              bool
	Extra                      []BroadcastConfig
	ControlPort                int
	DisableClientEncryption    bool
	NetworkDir, RejoinAddress  string
	JavaProxy, JavaDataDir     string
	JavaAuth                   *javaAuthOptions
	CrossServerChat            bool
	FakePlayerCount            int
	FakePlayerDrift            *PlayerDriftConfig
}

func defaultTemplateValues() templateValues {
	return templateValues{
		Mode: modeRelay, Bind: "0.0.0.0:19132", MOTD: "A Beacon Server", ShowMaxPlayers: 20,
		Servers:       []bridge.ServerConfig{{Name: "lobby", Address: "127.0.0.1:19133"}},
		Try:           []string{"lobby"},
		AcceptFriends: true, ControlPort: 7777,
	}
}

func renderTOML(v templateValues) string {
	tmpl := template.Must(template.New("beacon.toml").Funcs(template.FuncMap{"q": tomlQuote, "list": tomlList}).Parse(defaultTOMLTemplate))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, v); err != nil {
		panic(err) // the template is embedded, so this is a programming error
	}
	return buf.String()
}

// tomlQuote writes s as a TOML basic string ("..."), the quoting velocity.toml uses.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlList(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = tomlQuote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// migrateLegacyConfig writes beacon.toml from config.json and renames config.json, so it is read
// once.
func migrateLegacyConfig() error {
	old, err := loadConfig(legacyConfigFile)
	if err != nil {
		return err
	}
	v := templateValues{
		MOTD: old.WorldName, HostName: old.HostName, ShowMaxPlayers: old.MaxPlayers,
		Admins: old.Admins, AcceptFriends: !old.NoFriendAccept, ListingToggle: old.ListingToggle,
		Extra: old.ExtraBroadcasts, ControlPort: old.ControlPort,
		DisableClientEncryption: old.DisableClientEncryption,
		NetworkDir:              old.NetworkDir, RejoinAddress: old.RejoinAddress,
		JavaProxy: old.JavaProxy, JavaDataDir: old.JavaDataDir,
		FakePlayerCount: old.FakePlayerCount, FakePlayerDrift: old.FakePlayerDrift,
	}
	// nether2rak always served the chat routes; a converted config keeps them.
	v.CrossServerChat = true
	if old.JavaProxy != "" {
		// nether2rak always ran the premium/cracked Java logins, with these servers.
		v.JavaAuth = &javaAuthOptions{LoginServer: "pvp", ProtectedServer: "skygen", URL: "http://127.0.0.1:7797/auth/"}
	}
	if old.HostName == old.WorldName {
		v.HostName = ""
	}
	if old.RelayDirectIP {
		v.Bind = old.RelayDirectIPListenAddress
	}
	switch {
	case len(old.Servers) > 0:
		v.Mode, v.Servers = modeRelay, old.Servers
		def := old.DefaultServer
		if def == "" {
			def = old.Servers[0].Name
		}
		v.Try = []string{def}
	case strings.EqualFold(old.BackendTransport, bridge.TransportNetherNetNoRelay):
		v.Mode = modeRelayless
		v.Servers = []bridge.ServerConfig{{Name: "main", Address: "http://" + strings.TrimPrefix(old.NetherNetBackendAddress, "http://")}}
		v.Try = []string{"main"}
	default:
		v.Mode = modeRelay
		s := bridge.ServerConfig{Name: "main", Address: old.GeyserAddress, Transport: bridge.TransportRakNet}
		if usesNetherNet(old) {
			s = bridge.ServerConfig{Name: "main", Address: "http://" + strings.TrimPrefix(old.NetherNetBackendAddress, "http://")}
		}
		v.Servers, v.Try = []bridge.ServerConfig{s}, []string{"main"}
	}
	if err := os.WriteFile(tomlConfigFile, []byte(renderTOML(v)), 0o644); err != nil {
		return err
	}
	return os.Rename(legacyConfigFile, legacyConfigFile+".migrated")
}
