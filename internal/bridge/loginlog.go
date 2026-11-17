package bridge

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"

	"github.com/ezchr/beacon-proxy/internal/proxy"
)

// refusedLoginAttrs is who a refused login was, as far as its login said before the refusal. A
// refusal over the client data (a bad ServerAddress, skin or device field) comes after the Xbox
// sign-in chain was checked, so its account is real (identityChecked); an earlier refusal's
// identity can be anything the client wrote.
//
// A Bedrock client joining a world from the Friends tab or an invite sends an empty ServerAddress.
// Tools built on gophertunnel's dialer (bots, world downloaders) send the host's NetherNet or
// messaging id there instead, which the login check refuses: logging it names who they are.
func refusedLoginAttrs(c *proxy.ProxyConn, remote string, err error) []any {
	id, cd := c.IdentityData(), c.ClientData()
	if id.XUID == "" && id.DisplayName == "" && cd.ServerAddress == "" {
		return nil
	}
	return []any{
		"name", id.DisplayName, "xuid", id.XUID,
		"identityChecked", strings.Contains(err.Error(), "client data"),
		"serverAddress", cd.ServerAddress, "deviceOS", cd.DeviceOS, "deviceModel", cd.DeviceModel,
		"gameVersion", cd.GameVersion, "remote", remote,
	}
}

// logRefusedLogin logs a login Beacon refused, with refusedLoginAttrs.
func logRefusedLogin(log *slog.Logger, c *proxy.ProxyConn, remote string, err error) {
	log.Warn("client failed to log in", append([]any{"err", err}, refusedLoginAttrs(c, remote, err)...)...)
}

// nonceRefusal is what a world join is told when its login has no nonce from the world's sessions.
const nonceRefusal = "Could not join the world. Join it again from the Friends tab."

// worldNonceOK reports whether a login may go on under cfg's nonce check, logging a refusal.
func worldNonceOK(cfg Config, xuid, name string, cd login.ClientData, remote string, log *slog.Logger) bool {
	if cfg.CheckNonce == nil || cfg.DirectIP || cfg.CheckNonce(xuid, cd.Nonce) {
		return true
	}
	log.Warn("world join refused: the login carries no nonce from this world's sessions (the account never joined the world's Xbox session)",
		"name", name, "xuid", xuid, "nonceSent", cd.Nonce != "", "serverAddress", cd.ServerAddress,
		"deviceOS", cd.DeviceOS, "deviceModel", cd.DeviceModel, "remote", remote)
	return false
}

// logLoginSize logs how big a player's login was and what made it up. The skin travels inside the
// login (image, geometry, animation frames, cape, persona pieces), and some servers and proxies
// refuse a login above a size of their own ("login packet too long"): this tells which part of a
// skin makes a login big.
func logLoginSize(log *slog.Logger, raw []byte, cd login.ClientData) {
	anim := 0
	for _, a := range cd.AnimatedImageData {
		anim += len(a.Image)
	}
	log.Info("login size", "loginBytes", len(raw), "skinId", cd.SkinID,
		"skin", fmt.Sprintf("%dx%d", cd.SkinImageWidth, cd.SkinImageHeight), "skinDataBytes", len(cd.SkinData),
		"geometryBytes", len(cd.SkinGeometry), "resourcePatchBytes", len(cd.SkinResourcePatch),
		"animations", len(cd.AnimatedImageData), "animationBytes", anim, "capeBytes", len(cd.CapeData),
		"personaPieces", len(cd.PersonaPieces), "persona", cd.PersonaSkin, "premium", cd.PremiumSkin)
}
