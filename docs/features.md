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
- **Commands:** `/server` lists the servers and `/ping [player]` shows a ping (your own, a player's, or
  `@a` for everyone's), for everyone; admins also get `/glist`, `/send` and `/broadcast`. Beacon
  answers `/ping` itself, replacing a server's own; a name Beacon doesn't know goes on to the server.
  With a Java proxy reporting its Java players (see Bans and mutes), `/ping` and `/glist` include
  them, and the proxy can give Java players the same `/ping` through the control API.

## Server list

`ping-passthrough` decides what the server list shows: Beacon's own `motd` and player count
(`"off"`, the default), or those of the first server in `try` that answers: its name (`"motd"`), its
player counts (`"players"`) or both (`"all"`).

## Reloading

`reload` in the console, or `/beacon reload` in game for admins, rereads `beacon.toml` without a
restart. The servers, `try`, forced hosts, admins, ping passthrough, rate limits, `moderation`
and cross-server chat change at once; players stay where they are. It lists anything else you changed, which needs
a restart (`mode`, `bind`, `motd`, accounts and so on).

## Game versions

Each Beacon release serves one Bedrock version (this one: 1.26.60), and your servers run that same
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
  players online at once from one IP address; a player joining again from it still gets in. A
  good start:

  ```toml
  login-ratelimit = 500
  packets-per-second = 500
  sessions-per-ip = 4
  ```

  In our tests a player stayed under 100 packets a second, even moving between servers, so 500
  only stops floods. A player sent away for it reads "Too many packets.", and Beacon's log says `packet rate limit` with their name.

## Skins for Java players

When your servers also have Java players (Geyser with Floodgate, or Dragonfly's Java support),
Java clients need Bedrock players' skins as Java textures. Beacon uploads each player's skin to
GeyserMC's global skin API with the player's own Xbox-signed login, as Geyser does when players
join it directly, so the skin is there by XUID for those servers to look up. Set
`BEACON_NO_SKIN_UPLOAD=1` to turn it off.

## Java players

For Java Edition players, run Velocity (or another Java proxy) next to Beacon as usual.

## Bans and mutes

Beacon's admins have these across the whole network, in relay mode. There's nothing to set up:

```
/ban Steve 7d griefing
/ban "Some Player"
/banip Steve 30d
/mute Steve 1h spam
/unban Steve
/banlist
```

| Command | Does |
| --- | --- |
| `/ban <player> [time] [reason]` | turns the player away at login, and removes them if online |
| `/banip <player or ip> [time] [reason]` | the same for an address |
| `/mute <player> [time] [reason]` | stops their chat and `/msg`, `/tell`, `/me` |
| `/kick <player> [reason]` | disconnects them (they may join again at once); with `kick = "server"` in `beacon.toml`, it only takes them off their server, to another of yours |
| `/unban`, `/unbanip`, `/unmute` | lifts one |
| `/banlist`, `/mutelist` | lists them |

- **Time** is such as `30m`, `12h`, `7d`, `4w` or `3months`, or `perm`. Left out, it has no end. A
  time Beacon can't read, like `7x`, is refused instead of banning for good.
- **Names:** players online are suggested as you type, and `/unban` and `/unmute` suggest who is
  on the list; other names can be typed out. A gamertag with a space goes in quotes.
- **Offline players** work too, if they have joined before; they are saved in `moderation.json`. To
  ban an Xbox player who has never joined, use `/ban xbox:Gamertag`.
- **Console:** all of these work there without the slash, and through the
  [control API](control-api.md). The console can also ban an admin, which an admin in game can't.
- **Java players** (crossplay behind a Java proxy such as Velocity): the proxy can ask Beacon at
  each Java login and every few seconds after, through the control API's `/moderation/java/...`
  routes, so the same list covers them. A Java player who logged in is then found by name like a
  Bedrock player; when a Bedrock player has the same name, `/ban java:Steve` means the Java one.
  Java names prove nothing on an offline-mode server, so no Java player counts as an admin, and an
  address ban stops them all. The proxy can pass its own admins' commands to `/moderation` with
  `&by=<name>`; those can't ban Beacon's admins.
- **An address ban** doesn't stop admins, so one on a shared address can't lock them out.

For admins these replace a server's commands of the same name. Everyone else still gets the
server's own.

They cover players relayed through Beacon. Beacon can't reach a Java player who joins through a
Java proxy, so an admin's command with a name Beacon doesn't know is passed on to the server they
are on, whose own command can act on it. That only covers that server.

### Admins

Beacon's admins are the `admins` in `beacon.toml`, and anyone added from the console:

```
admin add Steve
admin remove Steve
admin list
```

This is console and control API only, so an admin in game can't make more admins. It works at once, with no reload; a new admin's commands show after they rejoin. Someone listed in `beacon.toml` has to be taken out there.

Being a Beacon admin has nothing to do with being an op on a server. Someone who isn't a Beacon admin gets their server's commands as usual.

### Turning it off

If your network has its own ban plugin, set `moderation = false` under `[advanced]`. Beacon then:

- stops answering `/ban`, `/banip`, `/mute` and the rest, which go to your servers instead, even from Beacon's admins;
- stops turning away players it banned, and stops blocking chat of players it muted;
- keeps what it saved, so turning it back on brings those bans and mutes back.

`reload` applies it. Other admin commands, such as `/send` and `/glist`, stay on.

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
