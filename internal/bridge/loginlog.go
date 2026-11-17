package bridge

import (
	"log/slog"
	"strings"

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
