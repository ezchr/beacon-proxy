package bridge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// clientToken makes an identity token issued at iat, with key's public half as its cpk.
func clientToken(t *testing.T, key *ecdsa.PrivateKey, iat time.Time) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES384, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{
		"iat": iat.Unix(),
		"nbf": iat.Unix(),
		"exp": iat.Add(time.Hour).Unix(),
		"cpk": base64.StdEncoding.EncodeToString(der),
	}
	s, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifyClientTokenClockLeeway(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		off  time.Duration
		ok   bool
	}{
		{"on time", 0, true},
		{"83 s fast (refused by the 1 minute default)", 83 * time.Second, true},
		{"110 s slow", -110 * time.Second, true},
		{"5 min fast", 5 * time.Minute, false},
	} {
		got, err := verifyClientToken(context.Background(), clientToken(t, key, time.Now().Add(c.off)))
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %v", c.name, err, c.ok)
			continue
		}
		if c.ok && !got.Equal(&key.PublicKey) {
			t.Errorf("%s: wrong public key", c.name)
		}
	}
}
