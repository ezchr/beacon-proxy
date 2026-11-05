package bridge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// clockLeeway is how far a joining client's clock may be off from the relay's, either way. The
// identity token a client presents carries times from its own clock, and the JWT default allows a
// minute only: a PC 83 s fast was refused with error 37 ("token issued in the future").
const clockLeeway = 2 * time.Minute

// verifyClientToken is go-nethernet's default client token check with clockLeeway: the token's
// times, and the public key of its cpk claim. Its signature is checked by the login, as with the
// default (its GameServerToken's cpk must hold the same key).
func verifyClientToken(_ context.Context, token string) (*ecdsa.PublicKey, error) {
	t, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES384, jose.RS256})
	if err != nil {
		return nil, fmt.Errorf("parse JWT token: %w", err)
	}
	var claims struct {
		jwt.Claims
		CPK json.RawMessage `json:"cpk"`
	}
	if err := t.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return nil, fmt.Errorf("extract JWT claims: %w", err)
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{Time: time.Now()}, clockLeeway); err != nil {
		return nil, fmt.Errorf("validate JWT claims: %w", err)
	}
	key, err := tokenPublicKey(claims.CPK)
	if err != nil {
		return nil, fmt.Errorf("parse cpk: %w", err)
	}
	return key, nil
}

// tokenPublicKey reads a cpk claim: a JWK (26.40+ clients) or base64 DER (older ones).
func tokenPublicKey(data []byte) (*ecdsa.PublicKey, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("no cpk claim")
	}
	var key any
	switch data[0] {
	case '{':
		var jwk jose.JSONWebKey
		if err := json.Unmarshal(data, &jwk); err != nil {
			return nil, err
		}
		key = jwk.Key
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, err
		}
		der, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("decode base64: %w", err)
		}
		if key, err = x509.ParsePKIXPublicKey(der); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("expected a JWK or a base64 string, got %q", data)
	}
	k, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("cpk is a %T, not an ECDSA key", key)
	}
	return k, nil
}
