package bridge

import (
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// Client-side chunk generation (BDS client-side-chunk-generation-enabled = true): a BDS server
// tells a client that says it can (its login's CompatibleWithClientSideChunkGen) to build the far
// chunks itself, from the seed, generator, biomes and FeatureRegistry of the StartGame it joined
// with, and sends only the chunks around the player (tested on 26.60: 51 chunks instead of 369 at
// radius 10). BDS forces it off for flat, void, old and world-template worlds whatever the setting.
//
// A client keeps the StartGame of its first server for the whole session, so it can only build
// the chunks of that server: on any other one it would show nothing where that server leaves the
// chunks to it. So every server is told the client cannot (it then sends all its chunks, as with
// the setting off), except a server with client-side-generation = true in beacon.toml whose
// StartGame the client got: there the client's own answer is passed on.

// clientSideGenFor reports what to tell server of the client's client-side generation: capable is
// the client's own answer, loginServer the server whose StartGame the client got ("" while it
// joins), joining whether this login is one the client may get the StartGame of (a first join).
func clientSideGenFor(server ServerConfig, loginServer string, joining, capable bool) bool {
	if !capable || !server.ClientSideGeneration {
		return false
	}
	return joining || strings.EqualFold(server.Name, loginServer)
}

// backendClientData is the client data a login to server carries: the player's, with
// CompatibleWithClientSideChunkGen set by clientSideGenFor. joining: a first join (switchSession.join).
// A split-screen guest plays in its host's world, so it goes by the host's first server.
func (s *switchSession) backendClientData(server ServerConfig, joining bool) login.ClientData {
	cd := s.cd
	loginServer := s.loginServer
	if s.host != nil {
		loginServer, joining = s.host.loginServer, false
	}
	cd.CompatibleWithClientSideChunkGen = clientSideGenFor(server, loginServer, joining, s.cd.CompatibleWithClientSideChunkGen)
	return cd
}
