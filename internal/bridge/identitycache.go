package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/df-mc/go-nethernet"
	"github.com/ezchr/beacon-proxy/internal/session"
)

// IdentityCache hands out one client identity for every backend dial while its token is valid. The token is
// the relay's own (it is not the player's), and its key signs each connection's own fingerprints, so one
// identity serves any number of connections. Asking Minecraft's authorization service on every join and
// every server move made each of them fail whenever that service was slow or down.
type IdentityCache struct {
	mint func(ctx context.Context) (*nethernet.Identity, error)
	log  *slog.Logger

	mu       sync.Mutex
	id       *nethernet.Identity
	expires  time.Time
	renewing bool
}

const (
	// identityRenewBefore is how long before its token expires an identity is renewed in the background.
	identityRenewBefore = 10 * time.Minute
	// identityMinLeft is the least validity an identity handed out must have left.
	identityMinLeft = time.Minute
	// identityAttempts is how often minting is tried before giving up, identityAttemptTimeout each.
	identityAttempts       = 3
	identityAttemptTimeout = 8 * time.Second
)

// NewIdentityCache returns an IdentityCache minting identities for the account of s.
func NewIdentityCache(s *session.Session, log *slog.Logger) *IdentityCache {
	return newIdentityCache(func(ctx context.Context) (*nethernet.Identity, error) {
		return NewClientIdentity(ctx, s)
	}, log)
}

func newIdentityCache(mint func(ctx context.Context) (*nethernet.Identity, error), log *slog.Logger) *IdentityCache {
	if log == nil {
		log = slog.Default()
	}
	return &IdentityCache{mint: mint, log: log}
}

// Identity returns the cached identity, minting one when there is none with at least identityMinLeft left.
// An identity close to expiry is still handed out while a new one is minted in the background.
func (c *IdentityCache) Identity(ctx context.Context) (*nethernet.Identity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	left := time.Until(c.expires)
	if c.id != nil && left > identityMinLeft {
		if left < identityRenewBefore && !c.renewing {
			c.renewing = true
			go c.renew()
		}
		return c.id, nil
	}
	id, err := c.mintRetrying(ctx)
	if err != nil {
		return nil, err
	}
	c.set(id)
	return id, nil
}

// renew mints the next identity before the current one expires.
func (c *IdentityCache) renew() {
	ctx, cancel := context.WithTimeout(context.Background(), identityAttempts*(identityAttemptTimeout+2*time.Second))
	defer cancel()
	id, err := c.mintRetrying(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renewing = false
	if err != nil {
		c.log.Warn("renew backend identity: keeping the current one", "err", err, "expires", c.expires)
		return
	}
	c.set(id)
}

// set stores id; c.mu must be held.
func (c *IdentityCache) set(id *nethernet.Identity) {
	c.id = id
	c.expires = tokenExpiry(id.Token)
}

func (c *IdentityCache) mintRetrying(ctx context.Context) (*nethernet.Identity, error) {
	var err error
	for attempt := range identityAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, err
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		actx, cancel := context.WithTimeout(ctx, identityAttemptTimeout)
		var id *nethernet.Identity
		id, err = c.mint(actx)
		cancel()
		if err == nil {
			return id, nil
		}
		c.log.Warn("mint backend identity failed", "attempt", attempt+1, "err", err)
	}
	return nil, err
}

// tokenExpiry reads the exp claim of a JWT without checking it: the token is our own, just issued. A token
// it cannot read is treated as valid for the next quarter of an hour.
func tokenExpiry(jwt string) time.Time {
	fallback := time.Now().Add(15 * time.Minute)
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return fallback
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return fallback
	}
	return time.Unix(claims.Exp, 0)
}
