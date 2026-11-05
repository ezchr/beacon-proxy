package bridge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/go-nethernet"
)

func testToken(exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return "e30." + payload + ".sig"
}

// TestIdentityCache: one identity serves every dial while valid, renewal near expiry happens in the
// background, and a failing authorization service does not fail dials while the old identity lasts.
func TestIdentityCache(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	exp := time.Now().Add(time.Hour)
	c := newIdentityCache(func(context.Context) (*nethernet.Identity, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("service down")
		}
		return &nethernet.Identity{Token: testToken(exp)}, nil
	}, nil)

	first, err := c.Identity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		id, err := c.Identity(context.Background())
		if err != nil || id != first {
			t.Fatalf("dial got a new identity: %v", err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d mints for 6 dials, want 1", n)
	}

	// Close to expiry with the service down: the old identity is still handed out.
	fail.Store(true)
	c.mu.Lock()
	c.expires = time.Now().Add(5 * time.Minute)
	c.mu.Unlock()
	id, err := c.Identity(context.Background())
	if err != nil || id != first {
		t.Fatalf("near expiry: %v, same %v", err, id == first)
	}

	// Expired with the service down: the dial fails after the retries.
	time.Sleep(10 * time.Millisecond)
	for {
		c.mu.Lock()
		renewing := c.renewing
		c.mu.Unlock()
		if !renewing {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.mu.Lock()
	c.expires = time.Now()
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Identity(ctx); err == nil {
		t.Fatal("expired identity handed out")
	}

	// Service back: a new identity.
	fail.Store(false)
	id, err = c.Identity(context.Background())
	if err != nil || id == first {
		t.Fatalf("after recovery: %v, new %v", err, id != first)
	}
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Unix(time.Now().Add(time.Hour).Unix(), 0)
	if got := tokenExpiry(testToken(exp)); !got.Equal(exp) {
		t.Fatalf("expiry %v, want %v", got, exp)
	}
	if got := tokenExpiry("garbage"); time.Until(got) < 10*time.Minute {
		t.Fatalf("unreadable token expiry %v", got)
	}
}
