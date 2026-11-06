# Features

Everything Beacon does besides the basics in the README. Most of it is on by default and needs no
setup.

## Moving between servers (relay mode)

- **`/server <name>`** moves a player without them leaving the game. Beacon keeps the player's
  connection and swaps the server behind it.
- **Different content per server.** Servers may have different resource packs, blocks and items.
  Beacon merges them for the client and renumbers blocks and items per server, so a player can go
  from a vanilla server to one with custom blocks. It caches each server's content in `network/`
  (`network-dir` under `[advanced]`). When a server's packs change while someone is joining, the
  player is asked to join again, or sent to `rejoin-address` if you set one.
- **Safe arrivals.** After a move Beacon holds back the client's movement until it stands at the
  new spawn, so the new server doesn't see a long fall, and it re-sends the chunks around the spawn
  a moment later, since a client still loading can drop them and leave an invisible hole.
- **Join order and failover:** players join the first server in `try` that can be joined. When a
  player's server stops or drops them, Beacon moves them to another server instead of
  disconnecting them.
- **Forced hosts:** players who join by a given address get their own order:

  ```toml
  [forced-hosts]
  "pvp.example.com" = ["pvp"]
  ```

  Friends-tab joins carry no address and always use `try`.
- **Commands:** `/server` lists the servers; admins also get `/glist`, `/send` and `/broadcast`.

## Server list

`ping-passthrough` decides what the server list shows: Beacon's own `motd` and player count
(`"off"`, the default), or those of the first server in `try` that answers: its name (`"motd"`), its
player counts (`"players"`) or both (`"all"`).

## Reloading

`reload` in the console, or `/beacon reload` in game for admins, rereads `beacon.toml` without a
restart. The servers, `try`, forced hosts, admins, ping passthrough, rate limits and cross-server
chat change at once; players stay where they are. It lists anything else you changed, which needs
a restart (`mode`, `bind`, `motd`, accounts and so on).

## Game versions

Each Beacon release serves one Bedrock version (1.0.0: 1.26.60), and your servers run that same
version. Players on another version get a message saying which version the server takes, instead
of a broken join.

## Protection

- **Xbox login check.** Every player is checked against Xbox Live before Beacon connects them, and
  Beacon runs the game's encryption handshake with each player, so a copied login token can't be
  replayed to join as someone else (`disable-client-encryption` under `[advanced]` turns this off;
  don't).
- **Signed logins** to your servers; see [forwarding](forwarding.md).
- **Command blocks and NPCs stay private.** A vanilla server sends every player the commands inside
  nearby command blocks and the commands behind NPC buttons, where world-saving tools read them.
  Beacon blanks them for everyone but its admins; the blocks and NPCs still work, since the server
  runs its own copy.
- **Datacenter block.** Put IP ranges (`203.0.113.0/24`) or single IPs, one a line (`#` starts a
  comment), in `dcblock/block.txt` to turn away joins
  from hosting providers, such as scanners on rented servers; `dcblock/allow.txt` lets ranges
  through anyway (Microsoft's, for example, which Xbox relays use). Every join attempt is logged in
  `dcblock/joins.log`. With no lists, nobody is blocked. `BEACON_DCBLOCK_DIR` moves the folder.
- **Allow list.** `allowed-xuids` under `[broadcast]` lets only those Xbox accounts join.
- **Rate limits** (under `[advanced]`, off by default): `login-ratelimit` is the least time in
  milliseconds between two joins from one IP address, `packets-per-second` the most packets a
  player may send in a second; a player past either is sent away. `sessions-per-ip` is the most
  players online at once from one IP address; a player joining again from it still gets in.

## Skins for Java players

When your servers also have Java players (Geyser with Floodgate, or Dragonfly's Java support),
Java clients need Bedrock players' skins as Java textures. Beacon uploads each player's skin to
GeyserMC's global skin API with the player's own Xbox-signed login, as Geyser does when players
join it directly, so the skin is there by XUID for those servers to look up. Set
`BEACON_NO_SKIN_UPLOAD=1` to turn it off.

## Java players

For Java Edition players, run Velocity (or another Java proxy) next to Beacon as usual.

## Bans and mutes

Admins have these across the whole network, in relay mode:

| Command | Does |
| --- | --- |
| `/ban <player> [time] [reason]` | turns the player away at login, and removes them if online |
| `/banip <player or ip> [time] [reason]` | the same for an address |
| `/mute <player> [time] [reason]` | stops their chat and `/msg`, `/tell`, `/me` |
| `/unban`, `/unbanip`, `/unmute` | lifts one |
| `/banlist`, `/mutelist` | lists them |

Time is such as `30m`, `12h`, `7d` or `4w`, or `perm`; left out, it has no end. They replace a server's commands of the same name for admins, and work for people who are offline. They are saved in `moderation.json`. They cover players relayed through Beacon, so not Java players joining through a Java proxy.

## Chat to Discord

Beacon can post chat to a Discord channel. Make a webhook in the
channel's settings and put it under `[advanced]`:

```toml
discord-webhook = "https://discord.com/api/webhooks/..."
discord-servers = ["survival"] # leave out for all servers
```

A server needs no plugin: Beacon reads what players relayed through it type. That covers Bedrock
players, and Beacon sees a message before the server does, so one the server would refuse, such as
from a muted player, is sent too. A server that sends its own chat to `/chat/send` is used instead,
which also covers Java players and leaves refused messages out.

Messages show the player's name and picture (Xbox gamerpic, or the skin's face for Java) when the
server names who spoke in `/chat/send`.

It only goes from the game to Discord. Colours
are removed and nothing a player types can ping anyone. `reload` applies a change.

## Cross-server chat

Off by default. With `cross-server-chat = true` under `[advanced]`, servers can share chat through
Beacon's [control API](control-api.md): each posts its players' messages to `/chat/send` and polls
`/chat/poll` for the other servers' lines. Beacon only carries the lines; each server needs a plugin
that sends and shows them.

## Player count

Beacon writes the number of Bedrock players it carries to `playercount/bedrock.txt` when it
changes, and adds a `playercount/java.txt` written by your Java proxy (a Velocity plugin, for
example), so every server list shows the network's total (`BEACON_PLAYERCOUNT_DIR` moves the folder). `fake-player-count` and
`fake-player-drift` under `[advanced]` show a fixed number, or one that wanders inside a range,
on the Friends tab instead of the real count.

## Hosting accounts and invites

See [Friends tab](friends-tab.md): extra hosting accounts (`beacon accounts add`), friend
requests, the 1000-friend and 30-player limits, and sending Xbox invites through the
[control API](control-api.md).
