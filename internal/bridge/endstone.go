package bridge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/df-mc/go-nethernet"
	"github.com/ezchr/beacon-proxy/internal/proxy"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// ForwardingEndstone is forwarding = "endstone" on a server: logins to it are signed for Endstone's
// [proxies.endgate], so the server gets the player's real XUID and address with no plugin.
const ForwardingEndstone = "endstone"

// The forwarding key for forwarding = "endstone", in the run folder: the private half stays here,
// the public half is what each such server needs as its keys/forwarding_key.pem.
const (
	endstoneKeyFile    = "endstone-forwarding.key"
	endstonePublicFile = "endstone-forwarding.pem"
)

// endstoneKey loads the forwarding key, making it the first time a server needs it.
func (n *Network) endstoneKey() (*ecdsa.PrivateKey, error) {
	n.endstoneOnce.Do(func() { n.endstoneK, n.endstoneErr = loadEndstoneKey(endstoneKeyFile, endstonePublicFile, n.Log) })
	return n.endstoneK, n.endstoneErr
}

// loadEndstoneKey reads the P-384 key in keyFile, or makes one, and (re)writes its public half to
// publicFile in the PEM form Endstone reads.
func loadEndstoneKey(keyFile, publicFile string, log *slog.Logger) (*ecdsa.PrivateKey, error) {
	var key *ecdsa.PrivateKey
	b, err := os.ReadFile(keyFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if key, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader); err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("%s is not a PEM key: delete it and Beacon makes a new one", keyFile)
		}
		if key, err = x509.ParseECPrivateKey(block.Bytes); err != nil {
			return nil, fmt.Errorf("%s: %w", keyFile, err)
		}
		if key.Curve != elliptic.P384() {
			return nil, fmt.Errorf("%s is not a P-384 key: delete it and Beacon makes a new one", keyFile)
		}
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(publicFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	if abs, err := filepath.Abs(publicFile); err == nil {
		publicFile = abs
	}
	log.Info(`endstone forwarding: copy this file to each forwarding = "endstone" server as keys/forwarding_key.pem, and set enabled = true under [proxies.endgate] in its endstone.toml`, "file", publicFile)
	return key, nil
}

// endstoneForwarding is how a login to a forwarding = "endstone" server is signed, and the
// NetherNet identity Beacon dials it with (the same key, as endgate does).
func (n *Network) endstoneForwarding(ctx context.Context, token, playerAddr string) (*proxy.EndstoneForwarding, func(context.Context) (*nethernet.Identity, error), error) {
	key, err := n.endstoneKey()
	if err != nil {
		return nil, nil, fmt.Errorf("endstone forwarding key: %w", err)
	}
	f := &proxy.EndstoneForwarding{Key: key, Token: token, Address: addressIP(playerAddr)}
	if token == "" {
		f.Template = n.probeClaims(ctx)
	}
	// A self-signed identity with the key as a base64 cpk, as endgate dials: BDS refuses the JWK form
	// (code 37).
	identity := func(context.Context) (*nethernet.Identity, error) { return nethernet.SelfSignedIdentity(key, "self") }
	return f, identity, nil
}

// probeClaims are the claims a made-up probe player's login starts from: those of Beacon's own
// account token, so they have every field a real login has.
func (n *Network) probeClaims(ctx context.Context) map[string]json.RawMessage {
	if n.Identity != nil {
		if id, err := n.Identity(ctx); err == nil && id != nil {
			if claims, err := proxy.JWTClaims(id.Token); err == nil {
				return claims
			}
		}
	}
	return map[string]json.RawMessage{"ipt": json.RawMessage(`"PlayFab"`)}
}

// addressIP is the IP of a player address: host:port, or a NetherNet connection description,
// whose last ip:port is where the player connected from. 127.0.0.1 when there is none.
func addressIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		host, _, _ = strings.Cut(host, "%")
		if net.ParseIP(host) != nil {
			return host
		}
	}
	if found := addrIP.FindAllStringSubmatch(addr, -1); len(found) > 0 {
		last := found[len(found)-1]
		if ip := net.ParseIP(last[1] + last[2]); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(addr); ip != nil {
		return ip.String()
	}
	return "127.0.0.1"
}

// playerLoginToken is the login token in a player's raw login packet (the one Microsoft signed).
func playerLoginToken(rawLogin []byte) string {
	id, payload, err := splitPacket(rawLogin)
	if err != nil || id != packet.IDLogin {
		return ""
	}
	var pk packet.Login
	if decodeInto(&pk, payload) != nil {
		return ""
	}
	return proxy.LoginToken(pk.ConnectionRequest)
}
