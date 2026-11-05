package forwarding

import (
	"encoding/json"
	"testing"
	"time"
)

func claimJSON(t *testing.T, c Claim) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"DeviceOS": 1, Field: c})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerify(t *testing.T) {
	secret, key := []byte("s"), "PUBKEY"
	c := Claim{Version: 1, XUID: "1", Name: "Steve", Server: "lobby", IssuedAt: time.Now().Unix(), ID: "a"}
	Sign(&c, key, secret)
	v := &Verifier{Secret: secret, Server: "lobby"}
	if _, err := v.Verify(claimJSON(t, c), key); err != nil {
		t.Fatal(err)
	}

	old := Claim{Version: 1, XUID: "1", Server: "lobby", IssuedAt: time.Now().Add(-5 * time.Minute).Unix(), ID: "b"}
	Sign(&old, key, secret)
	if _, err := v.Verify(claimJSON(t, old), key); err == nil {
		t.Fatal("accepted a claim from 5 minutes ago")
	}

	tampered := c
	tampered.ID, tampered.XUID = "c", "2"
	if _, err := v.Verify(claimJSON(t, tampered), key); err == nil {
		t.Fatal("accepted a changed claim")
	}

	if _, err := v.Verify([]byte(`{"DeviceOS":1}`), key); err != ErrNoClaim {
		t.Fatalf("no claim: got %v", err)
	}
}
