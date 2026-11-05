# Relay and relayless mode

Beacon runs in one of two modes, set with `mode` in `beacon.toml`.

| | `mode = "relay"` (default) | `mode = "relayless"` |
|---|---|---|
| Where the player's connection goes | to Beacon, which connects to your server for them | straight to your server; Beacon only introduces them |
| Servers | as many as you list | one |
| `/server`, `/send`, `/glist` | yes | no |
| Join by IP through Beacon (`bind`) | yes | no: players join your server's own address |
| Your server's Xbox check | off (`online-mode=false`): Beacon checks players | on (`online-mode=true`) |
| Who your server must accept | only Beacon: firewall it, use [forwarding](forwarding.md) | players, directly |
| When Beacon restarts | players are disconnected | players keep playing |
| Server must support | NetherNet or RakNet | NetherNet (BDS 1.26.60+, or a server that offers it) |

## Relay

The player connects to Beacon. Beacon checks their Xbox login, then logs into your server on their
behalf and passes the game traffic back and forth. Because Beacon holds the connection, it can move
the player to another server without them leaving the game, which is what `/server` does.

Your server sees Beacon's login, not the player's own, so it has to accept logins Beacon signs
rather than Xbox. That is why it runs with its Xbox check off and must only be reachable by Beacon.

Pick it when you have more than one server, or want players to join by IP through one address.

## Relayless

Beacon keeps the world on the Friends tab, and when a player clicks it, Beacon hands the player's
connection details to your server. The player then connects to your server directly, with their
own Xbox login, which your server checks itself. Beacon carries no game traffic.

Pick it when you only want your one server on the Friends tab and nothing else changes: the server
keeps working as it always has, and a Beacon restart does not kick anyone.
