package bridge

import (
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

func TestClientSideGenFor(t *testing.T) {
	csg := ServerConfig{Name: "Survival", ClientSideGeneration: true}
	plain := ServerConfig{Name: "kits"}
	cases := []struct {
		name        string
		server      ServerConfig
		loginServer string
		joining     bool
		capable     bool
		want        bool
	}{
		{"first join to an opted-in server keeps the client's answer", csg, "", true, true, true},
		{"a client that can't never can", csg, "", true, false, false},
		{"first join to a server without the option", plain, "", true, true, false},
		{"move to an opted-in server from another: it sends every chunk", csg, "kits", false, true, false},
		{"back to the opted-in server it joined on (any case)", csg, "survival", false, true, true},
		{"move away from it to a server without the option", plain, "Survival", false, true, false},
		{"move to another opted-in server", ServerConfig{Name: "other", ClientSideGeneration: true}, "Survival", false, true, false},
	}
	for _, c := range cases {
		if got := clientSideGenFor(c.server, c.loginServer, c.joining, c.capable); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBackendClientData(t *testing.T) {
	csg := ServerConfig{Name: "survival", ClientSideGeneration: true}
	host := &switchSession{cd: login.ClientData{CompatibleWithClientSideChunkGen: true, ThirdPartyName: "host"}}
	if !host.backendClientData(csg, true).CompatibleWithClientSideChunkGen {
		t.Fatal("first join to an opted-in server lost the client's answer")
	}
	if host.backendClientData(ServerConfig{Name: "kits"}, true).CompatibleWithClientSideChunkGen {
		t.Fatal("a server without the option was told the client generates chunks")
	}
	host.loginServer = "kits"
	if host.backendClientData(csg, false).CompatibleWithClientSideChunkGen {
		t.Fatal("a move to an opted-in server from another was told the client generates chunks")
	}
	if cd := host.backendClientData(csg, false); cd.ThirdPartyName != "host" {
		t.Fatal("the rest of the client data was not kept")
	}
	if !host.cd.CompatibleWithClientSideChunkGen {
		t.Fatal("the session's own client data was changed")
	}
	// A split-screen guest goes by its host's first server, even on its own first join.
	guest := &switchSession{cd: login.ClientData{CompatibleWithClientSideChunkGen: true}, host: host}
	if guest.backendClientData(csg, true).CompatibleWithClientSideChunkGen {
		t.Fatal("a guest whose host joined on kits was told to generate survival's chunks")
	}
	host.loginServer = "survival"
	if !guest.backendClientData(csg, true).CompatibleWithClientSideChunkGen {
		t.Fatal("a guest whose host joined on survival lost the client's answer there")
	}
}
