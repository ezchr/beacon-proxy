package proxy

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ezchr/beacon-proxy/forwarding"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// testClientData is client data that passes gophertunnel's login validation.
func testClientData() login.ClientData {
	d := login.ClientData{
		ThirdPartyName: "Steve", DeviceOS: protocol.DeviceAndroid, DeviceModel: "x",
		DefaultInputMode: packet.InputModeTouch, CurrentInputMode: packet.InputModeTouch,
		GameVersion: protocol.CurrentVersion, LanguageCode: "en_GB", PlayFabID: "0123456789abcdef",
		AnimatedImageData: []login.SkinAnimation{}, PersonaPieces: []login.PersonaPiece{},
		PieceTintColours: []login.PersonaPieceTintColour{}, SelfSignedID: uuid.NewString(), SkinID: uuid.NewString(),
		SkinData:        base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0, 0, 0, 255}, 64*64)),
		SkinImageHeight: 64, SkinImageWidth: 64,
		SkinResourcePatch:   base64.StdEncoding.EncodeToString([]byte(`{"geometry":{"default":"geometry.humanoid.custom"}}`)),
		SkinGeometryVersion: base64.StdEncoding.EncodeToString([]byte("0.0.0")), ArmSize: "wide", SkinColour: "#0",
	}
	d.DeviceID = d.ExpectedDeviceIDFormat().Generate()
	return d
}

func TestForwardingClaim(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	id := login.IdentityData{XUID: "2535400000000001", DisplayName: "Steve", Identity: "6e327061-0000-3000-8000-000000000001"}
	f := &Forwarding{Secret: []byte("secret"), Server: "lobby", PlayerAddr: "203.0.113.5:50000"}

	request, err := addForwardingClaim(login.EncodeOffline(id, testClientData(), key, false), newForwardingClaim(f, id, key), key)
	if err != nil {
		t.Fatal(err)
	}
	// The backend still parses it as a normal self-signed login.
	gotID, cd, _, err := login.Parse(request, nil)
	if err != nil {
		t.Fatal("login no longer parses:", err)
	}
	if gotID.XUID != id.XUID || cd.DeviceModel != "x" {
		t.Fatalf("login lost data: %+v %+v", gotID, cd)
	}

	_, raw, _ := splitRequest(request)
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(string(raw), ".")[1])
	pub := login.MarshalPublicKey(&key.PublicKey)
	v := &forwarding.Verifier{Secret: f.Secret, Server: "lobby"}
	if _, err := v.Verify(payload, pub); err != nil {
		t.Fatal("claim does not verify:", err)
	}
	if _, err := v.Verify(payload, pub); err == nil {
		t.Fatal("the same claim verified twice")
	}
	other, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, err := (&forwarding.Verifier{Secret: f.Secret}).Verify(payload, login.MarshalPublicKey(&other.PublicKey)); err == nil {
		t.Fatal("claim verifies for another connection's key")
	}
	for _, w := range []*forwarding.Verifier{{Secret: []byte("other")}, {Secret: f.Secret, Server: "skygen"}} {
		if _, err := w.Verify(payload, pub); err == nil {
			t.Fatalf("claim verifies with %+v", w)
		}
	}
}
