package proxy

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// A console logs a split-screen guest without an account in with AuthenticationType 1, the host's
// token and client data without game version, address, language, device model or PlayFab ID. The
// request here is synthetic, shaped like a real one.
func TestParseGuestSubClientLogin(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	cd := login.ClientData{
		DeviceOS: 12, SkinID: "skin", SkinImageWidth: 64, SkinImageHeight: 32,
		SkinData:          base64.StdEncoding.EncodeToString(make([]byte, 64*32*4)),
		SkinResourcePatch: base64.StdEncoding.EncodeToString([]byte(`{"geometry":{"default":"geometry.humanoid.custom"}}`)),
	}
	req := login.EncodeOffline(login.IdentityData{XUID: "2535400000000000", DisplayName: "Host"}, cd, key, false)
	n := binary.LittleEndian.Uint32(req)
	var m map[string]any
	if err := json.Unmarshal(req[4:4+n], &m); err != nil {
		t.Fatal(err)
	}
	m["AuthenticationType"] = 1
	delete(m, "Certificate")
	chain, _ := json.Marshal(m)
	var guest bytes.Buffer
	_ = binary.Write(&guest, binary.LittleEndian, int32(len(chain)))
	guest.Write(chain)
	guest.Write(req[4+n:])

	c := &ProxyConn{clientData: login.ClientData{GameVersion: "1.26.60", ServerAddress: "example.com:19132", LanguageCode: "en_US"}}
	id, got, auth, err := c.ParseLoginRequest(guest.Bytes())
	if err != nil {
		t.Fatalf("guest login: %v", err)
	}
	if !auth.Guest || id.XUID != "2535400000000000" || got.GameVersion != "1.26.60" {
		t.Fatalf("guest %v xuid %q version %q", auth.Guest, id.XUID, got.GameVersion)
	}
}
