# Relay and relayless mode

Beacon runs in one of two modes, set with `mode` in `beacon.toml`.

| | `mode = "relay"` (default) | `mode = "relayless"` |
|---|---|---|
| Where the player's connection goes | to Beacon, which connects to your server for them | straight to your server; Beacon only introduces them |
| Servers | as many as you list | one |
| `/server`, `/send`, `/glist` | yes | no |
| Join by IP through Beacon (`bind`) | yes | no: players join your server's own address |
| Bans, mutes, packet logs, reading chat for Discord | yes | no: Beacon doesn't see the game |
| Your server's Xbox check | off (`online-mode=false`): Beacon checks players | on (`online-mode=true`) |
| Who your server must accept | only Beacon: firewall it, use [forwarding](forwarding.md) | players, directly |
| When Beacon restarts | players are disconnected | players keep playing |

## Relay

The player connects to Beacon. Beacon checks their Xbox login, then logs into your server on their
behalf and passes the game traffic back and forth. Because Beacon holds the connection, it can move
the player to another server without them leaving the game, which is what `/server` does.

Your server sees Beacon's login, not the player's own, so it has to accept logins Beacon signs
rather than Xbox. That is why it runs with its Xbox check off and must only be reachable by Beacon.

Pick it when you have more than one server, or want players to join by IP through one address.

## Relayless

Beacon lists your server on the Friends tab as in relay mode, but when a player clicks it, Beacon
hands the player's connection details to your server instead of carrying the connection. The player then connects to your server directly, with their
own Xbox login, which your server checks itself. Beacon carries no game traffic.

Pick it when you don't need to move players between servers: you have one server and want it on
the Friends tab without changing anything on it. The server keeps working as it always has, a
Beacon restart does not kick anyone, and Beacon's machine carries no game traffic.
