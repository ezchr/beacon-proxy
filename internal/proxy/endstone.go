package proxy

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// Endstone forwarding: Endstone 0.12's [proxies.endgate] section in endstone.toml makes BDS trust
// one more login signing key beside Microsoft's. Beacon signs the player's own login claims with
// that key, so BDS checks the login like any online one and keeps the player's real XUID, and
// Endstone takes the player's address from the endgate:player_info claim. The format is endgate's,
// the proxy Endstone wrote it for: the player's Microsoft claims with a new cpk, iat and exp, the
// player info claim, signed ES384 with the key, whose base64 public key is the token's kid and the
// cpk that signs the client data.

// EndstoneForwarding is what a backend login for a forwarding = "endstone" server is signed with.
type EndstoneForwarding struct {
	// Key is Beacon's forwarding key, the public half of which the server has as its
	// keys/forwarding_key.pem.
	Key *ecdsa.PrivateKey
	// Token is the player's own login token (the one Microsoft signed), whose claims are
	// re-signed. Empty for Beacon's made-up probe players.
	Token string
	// Template holds the claims to start from when there is no Token: xid and xname are set from
	// the player's identity.
	Template map[string]json.RawMessage
	// Address is the player's IP address, without a port.
	Address string
}

// endstoneInfoClaim is the claim Endstone reads the forwarded player info from.
const endstoneInfoClaim = "endgate:player_info"

// SetEndstoneForwarding makes ForwardIdentity sign the login for Endstone's [proxies.endgate].
func (c *ProxyConn) SetEndstoneForwarding(f *EndstoneForwarding) { c.endstone = f }

// EndstoneKeyID is the name Endstone knows the key by: its public half as base64 DER, which is
// also the body of its PEM file.
func EndstoneKeyID(key *ecdsa.PublicKey) string { return login.MarshalPublicKey(key) }

// endstoneRequest turns a login request made by login.EncodeOffline with f.Key into one Endstone
// accepts: the client data stays (signed with the same key), the self-signed chain becomes a
// token signed with the forwarding key.
func endstoneRequest(f *EndstoneForwarding, id login.IdentityData, offline []byte, now time.Time) ([]byte, error) {
	_, clientData, err := splitRequest(offline)
	if err != nil {
		return nil, err
	}
	claims := map[string]json.RawMessage{}
	if f.Token != "" {
		if err := jwtClaims(f.Token, &claims); err != nil {
			return nil, fmt.Errorf("the player's login token: %w", err)
		}
	} else {
		maps.Copy(claims, f.Template)
		setClaim(claims, "xid", id.XUID)
		setClaim(claims, "xname", id.DisplayName)
	}
	kid := EndstoneKeyID(&f.Key.PublicKey)
	setClaim(claims, "cpk", kid)
	setClaim(claims, "iat", now.Unix())
	setClaim(claims, "exp", now.Add(time.Hour).Unix())
	setClaim(claims, endstoneInfoClaim, struct {
		Version int    `json:"version"`
		Address string `json:"address"`
	}{1, f.Address})
	payload, err := json.Marshal(claims)
	if err != nil {
		return nil, err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Key: f.Key, Algorithm: jose.ES384},
		&jose.SignerOptions{ExtraHeaders: map[jose.HeaderKey]any{"kid": kid}})
	if err != nil {
		return nil, err
	}
	sig, err := signer.Sign(payload)
	if err != nil {
		return nil, err
	}
	token, err := sig.CompactSerialize()
	if err != nil {
		return nil, err
	}
	auth, err := json.Marshal(struct {
		AuthenticationType uint8  `json:"AuthenticationType"`
		Token              string `json:"Token"`
	}{0, token}) // 0: a full (online) login
	if err != nil {
		return nil, err
	}
	buf := bytes.NewBuffer(nil)
	_ = binary.Write(buf, binary.LittleEndian, int32(len(auth)))
	buf.Write(auth)
	_ = binary.Write(buf, binary.LittleEndian, int32(len(clientData)))
	buf.Write(clientData)
	return buf.Bytes(), nil
}

// LoginToken returns the login token in a player's login request (the Token Microsoft signed), or
// "" when it has none.
func LoginToken(request []byte) string {
	auth, _, err := splitRequest(request)
	if err != nil {
		return ""
	}
	var r struct {
		Token string `json:"Token"`
	}
	if json.Unmarshal(auth, &r) != nil {
		return ""
	}
	return r.Token
}

// JWTClaims reads a JWT's claims without checking its signature.
func JWTClaims(token string) (map[string]json.RawMessage, error) {
	claims := map[string]json.RawMessage{}
	return claims, jwtClaims(token, &claims)
}

func jwtClaims(token string, v any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, v)
}

func setClaim(claims map[string]json.RawMessage, name string, v any) {
	claims[name], _ = json.Marshal(v)
}
