package bridge

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// Built-in client check: names the client a login comes from when its client data is a library's
// default fill-in rather than the game's, and refuses gophertunnel's defaults
// ([advanced] library-logins). The game always sends a device model and a real skin.
//
//   - gophertunnel: no device model, or the all-black skin its dialer fills in. Refused by default:
//     tools built on gophertunnel that do not copy a real client's data (scanners, bots).
//   - prismarinejs: PrismarineJS (bedrock-protocol): device model "PrismarineJS", minecraft-data's
//     Steve persona skin, one UUID as both DeviceId and SelfSignedId with PlayFabId cut from it,
//     Date.now() as ClientRandomId. Logged.
//   - cinnabar: the Cinnabar client, which joins Friends-tab worlds with its own target name
//     ("friend_xuid/<host XUID>") as the ServerAddress. Logged.
//
// Connections from this machine (Beacon's own tools) are never refused.

// Library login modes ([advanced] library-logins).
const (
	LibraryRefuse = iota // refuse gophertunnel's default client data (the default)
	LibraryLog           // only log the client
	LibraryOff           // neither
)

// LibraryLogins is the library-logins mode.
var LibraryLogins atomic.Int32

// LibraryExempt, when set, exempts a player (by XUID) from the refusal.
var LibraryExempt func(xuid string) bool

// libraryRefusal is what a refused login is told.
const libraryRefusal = "This client can't join: its login carries a library's default data, not a game's."

// defaultSkinData is the skin gophertunnel's dialer fills in when a client sets none: 64x32 pixels,
// all black.
var defaultSkinData = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0, 0, 0, 255}, 32*64))

// prismarineSkinID is the SkinId of the skin PrismarineJS logs in with when the bot sets none:
// minecraft-data's bedrock steve.json, one persona captured once.
const prismarineSkinID = "persona-ec4b0e51bc40322b-0"

// timestampClientID reports whether a ClientRandomId is a Unix time in milliseconds within a day
// of now, as PrismarineJS sends (Date.now()); the game sends a random 64-bit number.
func timestampClientID(id int64, now time.Time) bool {
	d := id - now.UnixMilli()
	return d > -86_400_000 && d < 86_400_000
}

// prismarineIDs reports whether a login's ids are PrismarineJS's: one UUID as both DeviceId and
// SelfSignedId, and its first 16 hex digits as PlayFabId (captured from bedrock-protocol 3.59.0).
// The game sends unrelated ids.
func prismarineIDs(cd login.ClientData) bool {
	if cd.DeviceID == "" || string(cd.DeviceID) != cd.SelfSignedID {
		return false
	}
	hex := strings.ReplaceAll(string(cd.DeviceID), "-", "")
	return len(hex) >= 16 && strings.EqualFold(cd.PlayFabID, hex[:16])
}

// clientKind names the client a login came from when it says so or carries a library's defaults:
// "cinnabar", "prismarinejs", "gophertunnel", or "" for the game (or a client copying it).
func clientKind(cd login.ClientData) string {
	switch {
	case strings.HasPrefix(cd.ServerAddress, login.CinnabarFriendTargetPrefix):
		return "cinnabar"
	case strings.EqualFold(strings.TrimSpace(cd.DeviceModel), "PrismarineJS") || cd.SkinID == prismarineSkinID || prismarineIDs(cd):
		return "prismarinejs"
	case strings.TrimSpace(cd.DeviceModel) == "" || cd.SkinData == defaultSkinData:
		return "gophertunnel"
	}
	return ""
}

// ownConn reports whether a connection comes from this machine: loopback or one of its own
// addresses (Beacon's own tools and bots).
func ownConn(addr net.Addr) bool {
	ip, _ := remoteIP(addr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	own, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range own {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// libraryLoginOK logs the client a login names and reports whether it may go on: false refuses
// gophertunnel's default client data (library-logins = "refuse").
func libraryLoginOK(id login.IdentityData, cd login.ClientData, remote net.Addr, world bool, log *slog.Logger) bool {
	mode := LibraryLogins.Load()
	if mode == LibraryOff {
		return true
	}
	kind := clientKind(cd)
	if kind == "" {
		return true
	}
	log.Info("client identified", "client", kind, "xuid", id.XUID, "name", id.DisplayName, "world", world,
		"serverAddress", cd.ServerAddress, "deviceOS", int(cd.DeviceOS), "deviceModel", cd.DeviceModel)
	if kind != "gophertunnel" || mode != LibraryRefuse || ownConn(remote) || (LibraryExempt != nil && LibraryExempt(id.XUID)) {
		return true
	}
	log.Warn("login refused: gophertunnel's default client data", "xuid", id.XUID, "name", id.DisplayName,
		"deviceModel", cd.DeviceModel, "remote", remote)
	return false
}
