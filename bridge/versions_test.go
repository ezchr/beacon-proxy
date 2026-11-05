package bridge

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ezchr/beacon-proxy/proxy"
	"github.com/sandertv/gophertunnel/minecraft"
)

// newerProto stands in for a 1.27.0 protocol: only its number matters here.
type newerProto struct{ minecraft.Protocol }

func (newerProto) ID() int32   { return 2300 }
func (newerProto) Ver() string { return "1.27.0" }

func TestServerVersions(t *testing.T) {
	proxy.RegisterProtocol(newerProto{minecraft.DefaultProtocol})
	pvp := ServerConfig{Name: "pvp"}
	skygen := ServerConfig{Name: "skygen", Protocols: []int32{2225}}
	if !pvp.Accepts(2225) || !pvp.Accepts(2300) || pvp.Accepts(2400) {
		t.Fatal("pvp should take every version the relay serves, and only those")
	}
	if !skygen.Accepts(2225) || skygen.Accepts(2300) {
		t.Fatal("skygen should take 1.26.60 only")
	}
	if got := skygen.versionMessage(2300); got != "Skygen doesn't support 1.27.0 yet." {
		t.Fatalf("message %q", got)
	}
	n := &Network{Servers: []ServerConfig{pvp, skygen}, Default: "skygen"}
	if s, ok := n.serverFor(2300); !ok || s.Name != "pvp" {
		t.Fatalf("1.27.0 player should join pvp, got %q %v", s.Name, ok)
	}
	if s, ok := n.serverFor(2225); !ok || s.Name != "skygen" {
		t.Fatalf("1.26.60 player should join the default, got %q", s.Name)
	}
	n.Servers = []ServerConfig{skygen}
	if _, ok := n.serverFor(2300); ok {
		t.Fatal("no server takes 1.27.0")
	}
	if p := (ServerConfig{Protocols: []int32{2300}}).probeProtocol(); p != 2300 {
		t.Fatalf("probe protocol %d", p)
	}
}

func TestServerListFitsClientVersion(t *testing.T) {
	proxy.RegisterProtocol(newerProto{minecraft.DefaultProtocol})
	p := &serverListProvider{cfg: DirectIPConfig{WorldName: "ZID", MaxPlayers: 30, Protocol: 2225, Version: "1.26.60"}, log: slog.Default()}
	for query, want := range map[string]string{
		"": `"protocol":2225,"version":"1.26.60"`,
		"?version=1.27.0&protocol=2300&platform=8&id=1": `"protocol":2300,"version":"1.27.0"`,
		"?version=1.28.0&protocol=2400":                 `"protocol":2225,"version":"1.26.60"`,
	} {
		got := string(p.body(httptest.NewRequest(http.MethodGet, "/v1/join"+query, nil)))
		if !strings.Contains(got, want) {
			t.Errorf("%q: got %s, want %s", query, got, want)
		}
	}
}
