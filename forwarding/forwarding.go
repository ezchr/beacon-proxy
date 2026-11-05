// Package forwarding is Beacon's player-info forwarding: the signed claim Beacon puts in each
// login it makes to a backend in relay mode, and the check a backend runs on it. A Go backend
// (Dragonfly, for example) imports this package; backends in other languages implement the same
// check from docs/forwarding.md.
package forwarding

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Field is the client-data key the claim is stored under.
const Field = "BeaconForwarding"

// Claim is the claim as it appears in the login's client data.
type Claim struct {
	Version  int    `json:"v"`
	XUID     string `json:"xuid"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
	IP       string `json:"ip"`
	Server   string `json:"server"`
	IssuedAt int64  `json:"iat"`
	ID       string `json:"jti"`
	MAC      string `json:"mac,omitempty"`
}

// MACInput is the exact byte string the MAC is computed over: the fields one per line, in this
// order, after a fixed prefix. identityPublicKey is the base64 key the login is signed with.
func MACInput(c Claim, identityPublicKey string) []byte {
	return []byte(strings.Join([]string{
		"beacon-forwarding-v1", c.XUID, c.Name, c.Identity, c.IP, c.Server,
		strconv.FormatInt(c.IssuedAt, 10), c.ID, identityPublicKey,
	}, "\n"))
}

// Sign sets c.MAC.
func Sign(c *Claim, identityPublicKey string, secret []byte) {
	m := hmac.New(sha256.New, secret)
	m.Write(MACInput(*c, identityPublicKey))
	c.MAC = base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// ValidMAC checks c.MAC in constant time.
func ValidMAC(c Claim, identityPublicKey string, secret []byte) bool {
	got, err := base64.StdEncoding.DecodeString(c.MAC)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(MACInput(c, identityPublicKey))
	return hmac.Equal(got, m.Sum(nil))
}

// Verifier checks the claims a backend receives. Use one per backend for its whole run: it
// remembers claim IDs to refuse a claim used twice.
type Verifier struct {
	// Secret is the contents of Beacon's forwarding.secret.
	Secret []byte
	// Server is this backend's name in Beacon's beacon.toml. Empty accepts any name.
	Server string
	// Window is how old (or how far in the future, for clock skew) a claim may be. Default 60s.
	Window time.Duration

	mu   sync.Mutex
	seen map[string]time.Time
}

// ErrNoClaim means the login carries no Beacon claim: it did not come through Beacon.
var ErrNoClaim = errors.New("login did not come through Beacon")

// Verify checks the claim in a login's client data (the decoded JWT payload, as JSON).
// identityPublicKey is the base64 key the login is signed with, which the backend also uses for
// the encryption handshake; the backend must never skip that handshake, or a copied login could
// be replayed.
func (v *Verifier) Verify(clientData []byte, identityPublicKey string) (Claim, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(clientData, &data); err != nil {
		return Claim{}, fmt.Errorf("client data: %w", err)
	}
	raw, ok := data[Field]
	if !ok {
		return Claim{}, ErrNoClaim
	}
	var c Claim
	if err := json.Unmarshal(raw, &c); err != nil {
		return Claim{}, fmt.Errorf("beacon claim: %w", err)
	}
	if c.Version != 1 {
		return Claim{}, fmt.Errorf("beacon claim version %d is not supported", c.Version)
	}
	if !ValidMAC(c, identityPublicKey, v.Secret) {
		return Claim{}, errors.New("beacon claim signature does not match: is forwarding.secret the same on both sides?")
	}
	if v.Server != "" && c.Server != v.Server {
		return Claim{}, fmt.Errorf("beacon claim is for server %q, not %q", c.Server, v.Server)
	}
	window := v.Window
	if window <= 0 {
		window = time.Minute
	}
	now := time.Now()
	if age := now.Sub(time.Unix(c.IssuedAt, 0)); age > window || age < -window {
		return Claim{}, fmt.Errorf("beacon claim is %s old: check both machines' clocks (NTP)", age.Round(time.Second))
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.seen == nil {
		v.seen = map[string]time.Time{}
	}
	for id, t := range v.seen {
		if now.Sub(t) > 2*window {
			delete(v.seen, id)
		}
	}
	if _, used := v.seen[c.ID]; used {
		return Claim{}, errors.New("beacon claim was already used")
	}
	v.seen[c.ID] = now
	return c, nil
}
