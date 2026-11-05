package bridge

import (
	"log/slog"
	"net"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// PlayerChecks lets a build add its own checks on the players who join through the relay (relay
// mode), for example against world-downloading tools. Beacon itself ships none.
type PlayerChecks interface {
	// Login runs once a player's Xbox login is verified. unchecked leaves the player out of later
	// checks; a non-empty kick refuses them with that message.
	Login(id login.IdentityData, cd login.ClientData, remote net.Addr, log *slog.Logger) (unchecked bool, kick string)
	// ViewDistance runs on each view distance the player's client asks for; first is true the first
	// time. A non-empty kick disconnects them with that message.
	ViewDistance(id login.IdentityData, cd login.ClientData, radius int32, max uint8, first bool, log *slog.Logger) (kick string)
}

// Checks is the build's PlayerChecks, or nil for none.
var Checks PlayerChecks
