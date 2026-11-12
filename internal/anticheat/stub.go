// Package anticheat is the hook Beacon calls to check Bedrock players as their packets pass
// through. This public tree ships only this no-op stand-in: New returns nil, so no player is
// checked and the session code skips every call. A build with checks replaces this file with a
// package of the same API.
package anticheat

import (
	"log/slog"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Check names one check.
type Check string

// Logger and Alert are set by Beacon at start: where checks log, and how staff are told.
var (
	Logger = slog.Default()
	Alert  func(msg string)
)

// Load reads the anticheat config. The stand-in has none.
func Load() error { return nil }

// Enabled reports whether any check runs. Always false here.
func Enabled() bool { return false }

// Checks lists the checks that run.
func Checks() []Check { return nil }

// Conn is a player's session as the anticheat sees it.
type Conn interface {
	// SendClient sends the player a packet.
	SendClient(pk packet.Packet)
	// Disconnect kicks the player with msg.
	Disconnect(msg string)
}

// Player is one player's anticheat state.
type Player struct {
	// SelfRID is the player's own runtime ID on the client.
	SelfRID uint64
	// Datacenter is set when the player joined from a datacenter network.
	Datacenter bool
	// GameMode is the player's game mode as of the StartGame.
	GameMode int32
}

// New returns the player's state, or nil when the anticheat is off (always, here).
func New(conn Conn, id login.IdentityData, cd login.ClientData, log *slog.Logger) *Player {
	return nil
}

// Login reads the player's raw Login packet.
func (p *Player) Login(rawLogin []byte) {}

// ClientPacket sees a client packet; false drops it.
func (p *Player) ClientPacket(id uint32, payload []byte) (keep bool) { return true }

// ServerBatch sees a batch from server and returns packets to add to it.
func (p *Player) ServerBatch(server string, pks [][]byte) (extra []packet.Packet) { return nil }
