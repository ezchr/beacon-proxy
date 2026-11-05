package proxy

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ezchr/beacon-proxy/forwarding"
	"github.com/go-jose/go-jose/v4"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// Beacon forwarding (see package forwarding and docs/forwarding.md): in relay mode Beacon adds a
// signed claim to the client data of each backend login, so the backend can tell the login really
// came from Beacon.

// Forwarding is what a connection signs its claim with.
type Forwarding struct {
	Secret []byte
	// Server is the backend's name in beacon.toml, so a claim for one server is refused by another.
	Server string
	// PlayerAddr is the player's address as Beacon sees it.
	PlayerAddr string
}

// newForwardingClaim builds and signs the claim for a login made with key.
func newForwardingClaim(f *Forwarding, id login.IdentityData, key *ecdsa.PrivateKey) forwarding.Claim {
	jti := make([]byte, 16)
	_, _ = rand.Read(jti)
	c := forwarding.Claim{
		Version: 1, XUID: id.XUID, Name: id.DisplayName, Identity: id.Identity,
		IP: f.PlayerAddr, Server: f.Server, IssuedAt: time.Now().Unix(), ID: hex.EncodeToString(jti),
	}
	forwarding.Sign(&c, login.MarshalPublicKey(&key.PublicKey), f.Secret)
	return c
}

// addForwardingClaim puts the claim into an encoded login request (as built by
// login.EncodeOffline with key), re-signing the client data JWT with the same key.
func addForwardingClaim(request []byte, claim forwarding.Claim, key *ecdsa.PrivateKey) ([]byte, error) {
	chainJSON, rawToken, err := splitRequest(request)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(rawToken), ".")
	if len(parts) != 3 {
		return nil, errors.New("client data is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("client data payload: %w", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, fmt.Errorf("client data payload: %w", err)
	}
	if data[forwarding.Field], err = json.Marshal(claim); err != nil {
		return nil, err
	}
	payload, _ = json.Marshal(data)

	signer, err := jose.NewSigner(jose.SigningKey{Key: key, Algorithm: jose.ES384}, &jose.SignerOptions{
		ExtraHeaders: map[jose.HeaderKey]any{"x5u": login.MarshalPublicKey(&key.PublicKey)},
	})
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
	buf := bytes.NewBuffer(nil)
	_ = binary.Write(buf, binary.LittleEndian, int32(len(chainJSON)))
	buf.Write(chainJSON)
	_ = binary.Write(buf, binary.LittleEndian, int32(len(token)))
	buf.WriteString(token)
	return buf.Bytes(), nil
}

// splitRequest splits a login request into its chain JSON and client data JWT, both of which are
// int32 little-endian length prefixed.
func splitRequest(b []byte) (chainJSON, rawToken []byte, err error) {
	read := func() ([]byte, error) {
		if len(b) < 4 {
			return nil, errors.New("login request too short")
		}
		n := int(int32(binary.LittleEndian.Uint32(b)))
		if n < 0 || len(b)-4 < n {
			return nil, errors.New("login request length out of range")
		}
		v := b[4 : 4+n]
		b = b[4+n:]
		return v, nil
	}
	if chainJSON, err = read(); err != nil {
		return nil, nil, err
	}
	rawToken, err = read()
	return chainJSON, rawToken, err
}
