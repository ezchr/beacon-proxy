# Player-info forwarding

In relay mode your servers get Beacon's login, not the player's. Forwarding is how Beacon tells a
server who the player really is, and proves the login came through Beacon. Each server in
`beacon.toml` picks one of two ways with `forwarding`:

| `forwarding` | What the server gets | For |
|---|---|---|
| left out (or `"beacon"`) | Beacon's login, signed with `forwarding.secret` (this page) | Dragonfly, Geyser, BDS with xuidforward; servers that don't check it ignore it |
| `"endstone"` | the player's own login, re-signed with Beacon's key, which the server trusts like Microsoft's | BDS with Endstone 0.12 or newer; see [Endstone 0.12 and newer](../README.md#endstone-012-and-newer) |

The rest of this page is about the first: the server runs with its own Xbox check off, and Beacon
signs each login with a secret only it and your servers know, so a server that checks the
signature refuses every login that did not come through Beacon. It is the Bedrock version of
Velocity's modern forwarding.

## Do you need it?

- **Beacon on the same machine as the server:** no. Bind the server to `127.0.0.1` and nobody else
  can reach it.
- **Beacon on another machine, and you can add code to the server:** yes, add the check below.
- **Beacon on another machine, and you can't change the server:** firewall the server so it only
  accepts connections from Beacon's IP. That protects it without any code.

The check runs on the server because the danger is a connection that skips Beacon: Beacon never
sees it, only the server does.

## The secret

In relay mode Beacon always signs. It creates `forwarding.secret` with a random secret the first
time it starts (`forwarding-secret-file` in `beacon.toml` names the file). Keep it private, and copy
the same secret to each server's check. To keep the secret out of files, set the environment
variable `BEACON_FORWARDING_SECRET` instead.

A server that does not check yet simply ignores the signature, so nothing breaks before every
server has a check.

| Server | Check |
|---|---|
| Dragonfly, or any server written in Go | the `forwarding` package of this repository (below) |
| Others | write one from the format below; it is short |

Forwarding does not replace a firewall. Keep your servers reachable only by Beacon as well.

## Clocks

A signature is valid for one minute, so the machines running Beacon and your servers need correct
clocks. On Linux, turn on NTP (`timedatectl set-ntp true`). A server that refuses logins with "claim
is ... old" has a clock that is off.

## The format

Beacon adds this object to the client data of the login (the second JWT of the Login packet's
connection request), under the key `BeaconForwarding`:

```json
"BeaconForwarding": {
  "v": 1,
  "xuid": "2535400000000000",
  "name": "Steve",
  "identity": "uuid of the player",
  "ip": "203.0.113.5:50000",
  "server": "lobby",
  "iat": 1793000000,
  "jti": "32 hex characters, random",
  "mac": "base64 HMAC-SHA256"
}
```

- `ip` is the player's address as Beacon sees it.
- `server` is the server's name in `beacon.toml`.
- `iat` is when the login was made, in Unix seconds.
- `mac` is HMAC-SHA256, keyed with the secret's bytes, over these lines joined with `\n`:

  ```
  beacon-forwarding-v1
  <xuid>
  <name>
  <identity>
  <ip>
  <server>
  <iat>
  <jti>
  <identityPublicKey>
  ```

  `identityPublicKey` is the base64 public key the login is signed with, the same key the server
  encrypts the session to.

A server accepts the login only if all of these hold:

1. `v` is 1 and `mac` matches (compare in constant time).
2. `server` is the server's own name.
3. `iat` is within 60 seconds of the server's clock, either way.
4. `jti` has not been seen in the last two minutes.
5. The server runs the normal encryption handshake with the login's key. Never turn that off: the
   key is what makes a copied login useless, since only Beacon holds the private half.

Then the server can trust `xuid`, `name` and `ip`.

## Go servers

```go
import "github.com/ezchr/beacon-proxy/forwarding"

v := &forwarding.Verifier{Secret: secret, Server: "lobby"}

// clientData: the decoded client data JWT payload; key: the login's identityPublicKey.
claim, err := v.Verify(clientData, key)
if err != nil {
	// refuse the login: err says why
}
```

Use one `Verifier` for the whole run of the server: it remembers used `jti`s.
