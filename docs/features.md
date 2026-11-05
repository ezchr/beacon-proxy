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
- **Commands:** `/server` lists the servers; admins also get `/glist`, `/send` and `/broadcast`.

## Game versions

Beacon serves the current Bedrock version. Per server you can say which client versions it takes,
and which newer versions it speaks itself; for the rest Beacon translates, so a server that is one
version behind still takes players on the new one:

```toml
[servers]
old = { address = "127.0.0.1:19140", protocols = [2193, 2225] }
```

`protocols` lists the protocol numbers the server takes (default: all Beacon serves); `native` the
newer ones it speaks itself. A player on a version a server doesn't take gets a clear message
instead of a broken join.

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

## Skins for Java players

When your servers also have Java players (Geyser with Floodgate, or Dragonfly's Java support),
Java clients need Bedrock players' skins as Java textures. Beacon uploads each player's skin to
GeyserMC's global skin API with the player's own Xbox-signed login, as Geyser does when players
join it directly, so the skin is there by XUID for those servers to look up. Set
`BEACON_NO_SKIN_UPLOAD=1` to turn it off.

## Java players

For Java Edition players, run Velocity (or another Java proxy) next to Beacon as usual.

A Java proxy built into Beacon (`java-proxy` under `[advanced]`, using
[Gate](https://github.com/minekube/gate)) is **unfinished and not supported yet**; don't use it.

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
