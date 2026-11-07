package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

func TestEndstoneLoginCarriesThePlayersClaims(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	microsoft, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	player := signedJWT(t, microsoft, map[string]any{"xid": "2535400000000001", "xname": "Steve", "mid": "ABC", "cpk": "old", "exp": 1})

	id := login.IdentityData{XUID: "2535400000000001", DisplayName: "Steve", Identity: "6e327061-636b-3300-8000-000000000001"}
	offline := login.EncodeOffline(id, login.ClientData{ThirdPartyName: "Steve"}, key, false)
	now := time.Unix(1_800_000_000, 0)
	req, err := endstoneRequest(&EndstoneForwarding{Key: key, Token: player, Address: "203.0.113.5"}, id, offline, now)
	if err != nil {
		t.Fatal(err)
	}

	auth, clientData, err := splitRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		AuthenticationType *uint8
		Token              string
		Certificate        any
	}
	if err := json.Unmarshal(auth, &r); err != nil {
		t.Fatal(err)
	}
	if r.AuthenticationType == nil || *r.AuthenticationType != 0 || r.Certificate != nil {
		t.Fatalf("want a full login with only a token: %s", auth)
	}
	tok, err := jose.ParseSigned(r.Token, []jose.SignatureAlgorithm{jose.ES384})
	if err != nil {
		t.Fatal(err)
	}
	kid := EndstoneKeyID(&key.PublicKey)
	if tok.Signatures[0].Header.KeyID != kid {
		t.Fatalf("kid %q, want the key's base64 public key", tok.Signatures[0].Header.KeyID)
	}
	payload, err := tok.Verify(&key.PublicKey)
	if err != nil {
		t.Fatalf("the token must verify with the forwarding key: %v", err)
	}
	var c struct {
		XID, XName, MID, CPK string
		Exp, Iat             int64
		Info                 struct {
			Version int
			Address string
		} `json:"endgate:player_info"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	if c.XID != "2535400000000001" || c.XName != "Steve" || c.MID != "ABC" {
		t.Fatalf("the player's claims must carry over: %s", payload)
	}
	if c.CPK != kid || c.Iat != now.Unix() || c.Exp != now.Add(time.Hour).Unix() {
		t.Fatalf("cpk, iat and exp must be new: %s", payload)
	}
	if c.Info.Version != 1 || c.Info.Address != "203.0.113.5" {
		t.Fatalf("player info: %s", payload)
	}
	cd, err := jose.ParseSigned(string(clientData), []jose.SignatureAlgorithm{jose.ES384})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cd.Verify(&key.PublicKey); err != nil {
		t.Fatalf("the client data must be signed by the cpk's key: %v", err)
	}
}

func TestEndstoneLoginForAProbeUsesTheTemplate(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	id := login.IdentityData{XUID: "9", DisplayName: "beaconprobe"}
	tmpl := map[string]json.RawMessage{"ipt": json.RawMessage(`"PlayFab"`), "xid": json.RawMessage(`"1"`)}
	req, err := endstoneRequest(&EndstoneForwarding{Key: key, Template: tmpl, Address: "127.0.0.1"}, id,
		login.EncodeOffline(id, login.ClientData{}, key, false), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := LoginToken(req); got == "" {
		t.Fatal("no token")
	} else if claims, err := JWTClaims(got); err != nil || string(claims["xid"]) != `"9"` || string(claims["ipt"]) != `"PlayFab"` {
		t.Fatalf("claims %v %v", claims, err)
	}
	if string(tmpl["xid"]) != `"1"` {
		t.Fatal("the template must not change")
	}
}

func signedJWT(t *testing.T, key *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Key: key, Algorithm: jose.ES384}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(claims)
	sig, err := signer.Sign(b)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := sig.CompactSerialize()
	if strings.Count(s, ".") != 2 {
		t.Fatal("bad jwt")
	}
	return s
}
