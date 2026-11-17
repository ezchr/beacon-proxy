package bridge

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

type textAddrCC string

func (textAddrCC) Network() string  { return "nethernet" }
func (a textAddrCC) String() string { return string(a) }

// The PrismarineJS login as captured from bedrock-protocol 3.59.0 (offline, defaults).
var prismarineLogin = login.ClientData{DeviceModel: "PrismarineJS", DeviceOS: 12,
	DeviceID: "37b37b29-559a-3a40-8ce0-de86d0a20af6", SelfSignedID: "37b37b29-559a-3a40-8ce0-de86d0a20af6",
	PlayFabID: "37b37b29559a3a40", SkinID: prismarineSkinID, SkinData: "c2tpbg==", ServerAddress: "127.0.0.1:19998"}

func TestBuiltinClientKind(t *testing.T) {
	game := login.ClientData{DeviceModel: "iPhone14,2", ServerAddress: ":0", SkinData: "c2tpbg==",
		DeviceID: "1b1d4a27-3a0d-4a1d-9a64-2b6c2b0a7f01", SelfSignedID: "6f2c0c1e-2b8b-4e2d-8d0f-0a4f7c9e1d22"}
	for want, cd := range map[string]login.ClientData{
		"":             game,
		"cinnabar":     {DeviceModel: "83DK", ServerAddress: "friend_xuid/2535470912054074", SkinData: "c2tpbg=="},
		"prismarinejs": prismarineLogin,
		"gophertunnel": {ServerAddress: ":0", SkinData: "c2tpbg=="},
	} {
		if got := clientKind(cd); got != want {
			t.Errorf("%q: got %q", want, got)
		}
	}
	if !timestampClientID(time.Now().UnixMilli(), time.Now()) || timestampClientID(-4611686018427387904, time.Now()) {
		t.Error("timestamp client id")
	}
}

// gophertunnel's defaults are refused unless the mode says otherwise, the connection is this
// machine's, or the player is exempt; the other kinds are only logged.
func TestLibraryLoginOK(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	defer LibraryLogins.Store(LibraryRefuse)
	id := login.IdentityData{XUID: "1", DisplayName: "bot"}
	gt := login.ClientData{SkinData: defaultSkinData}
	far := net.Addr(textAddrCC("1 (2) (udp4 host 203.0.113.9:1234 (resolved: 203.0.113.9:1234))"))
	near := net.Addr(textAddrCC("1 (2) (udp4 host 127.0.0.1:1234 (resolved: 127.0.0.1:1234))"))
	LibraryLogins.Store(LibraryRefuse)
	if libraryLoginOK(id, gt, far, true, log) {
		t.Error("gophertunnel defaults let in")
	}
	if !libraryLoginOK(id, gt, near, true, log) {
		t.Error("this machine refused")
	}
	if !libraryLoginOK(id, prismarineLogin, far, true, log) {
		t.Error("PrismarineJS refused (it is only logged)")
	}
	LibraryLogins.Store(LibraryLog)
	if !libraryLoginOK(id, gt, far, true, log) {
		t.Error("log mode refused")
	}
	LibraryLogins.Store(LibraryRefuse)
	LibraryExempt = func(x string) bool { return x == "1" }
	defer func() { LibraryExempt = nil }()
	if !libraryLoginOK(id, gt, far, true, log) {
		t.Error("exempt player refused")
	}
}
