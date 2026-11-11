# Beacon Proxy

*Formerly **nether2rak**. If you used nether2rak, see [Migrating from nether2rak](docs/migrating.md).*

Beacon is a proxy for Minecraft: Bedrock Edition. Put it in front of your servers and it:

- **moves players between servers** with `/server`, without them leaving the game (like Velocity
  does for Java Edition);
- **lists your server on the Friends tab**, so players join from their friends list on any device,
  consoles included;
- **lets players join by IP** from the server list.

It works with Bedrock servers that speak NetherNet: Bedrock Dedicated Server (BDS), Dragonfly,
Geyser and others. It can run on the same machine as your servers or on its own.

## Quick start

1. Download `beacon` for your system from the [latest release](../../releases/latest) and put it in
   an empty folder.
2. Run it once: `./beacon` (Windows: `beacon.exe`). It writes `beacon.toml` and
   `forwarding.secret`, says what to do next, and stops.
3. Open `beacon.toml` and list your servers:

   ```toml
   [servers]
   lobby = "127.0.0.1:19133"
   survival = "127.0.0.1:19134"
   try = ["lobby"]
   ```

4. Set up each server for Beacon (next section).
5. Start Beacon again. It asks you to sign in with a Microsoft account: open the link it prints
   and enter the code. **Don't use your main Xbox account:** make a separate one for Beacon. The
   world is shown as hosted by it, and everyone who wants to see it on the Friends tab has to be its
   friend.

Players now find the world on the Friends tab once they friend the Beacon account, or join by your
IP and port 19132.

## Setting up your servers

In relay mode (the default) Beacon logs into your servers for the players, so each server must
accept Beacon's login: turn its own Xbox check off. Beacon has already checked the player.

| Server | Setting |
|---|---|
| BDS | `online-mode=false` in `server.properties` (`true` with `forwarding = "endstone"`), and see [Forwarding](#forwarding-who-the-player-is) for players' XUIDs. Next to other kinds of servers, also `block-network-ids-are-hashes=false`: players can't move between servers that number blocks differently. |
| Dragonfly | `AuthEnabled = false` under `[Server]` |
| Geyser | `validate-bedrock-login: false` (under `advanced:`, `bedrock:`); Floodgate features then stop working |

With its Xbox check off, a server lets in anyone who reaches it, so only Beacon may reach it.

### Example: Beacon on the same machine

```toml
[servers]
lobby = "127.0.0.1:19133"
survival = "127.0.0.1:19134"
try = ["lobby"]
```

Keep the servers' ports closed to the internet (bind them to `127.0.0.1`, or firewall the ports).
Players only ever connect to Beacon.

### Example: Beacon on another machine

Beacon on `203.0.113.5`, a server on `198.51.100.7`:

```toml
[servers]
lobby = "198.51.100.7:19133"
try = ["lobby"]
```

On the server's machine, let only Beacon in. NetherNet carries the game over UDP on changing ports,
so allow everything from Beacon's IP rather than one port, and nothing else to the server:

```
ufw allow from 203.0.113.5
```

If you can add code to the server, also have it check Beacon's signature on each login (see
[forwarding](docs/forwarding.md)); then even a connection that gets past the firewall is refused.

### Server options

A server is either just its address, or a table with more options:

```toml
[servers]
lobby = "127.0.0.1:19133"
survival = { address = "127.0.0.1:19134", forwarding = "endstone" }
```

| Option | Meaning |
|---|---|
| `address` | where Beacon reaches the server (`host:port`); the only one you must give |
| `forwarding` | how the server learns who the player is: leave it out for Beacon's signature, or `"endstone"`. See [Forwarding](#forwarding-who-the-player-is). |
| `resource-packs` | `false` leaves the server's resource packs out of what players get ([below](#leaving-out-a-servers-resource-packs)) |
| `transport` | `"nethernet"` (default), `"raknet"` for a server that only speaks RakNet, or `"tcp"` for a server on the same machine that listens on TCP ([below](#tcp-for-servers-on-the-same-machine)) |
| `encryption`, `compression` | `false` asks a `"tcp"` server not to encrypt or compress the link ([below](#tcp-for-servers-on-the-same-machine)) |
| `protocols` | the game versions (protocol numbers) the server takes, e.g. `[2193]`; leave it out for every version Beacon serves |
| `native` | newer versions the server speaks itself; players on the other versions it takes are translated by Beacon |
| `hold` | how long to keep the server's players on Beacon while it restarts, e.g. `"90s"` ([below](#holding-players-while-a-server-restarts)); leave it out to move them to another server at once |
| `hold-message`, `hold-reasons` | the title held players see, and more kick messages that mean the server is restarting ([below](#holding-players-while-a-server-restarts)) |

### TCP for servers on the same machine

A server on the same machine as Beacon can be reached over TCP instead of RakNet. RakNet spends work
on acknowledgements, resends and splitting packets that a local TCP link does not need, so Beacon
uses less CPU (about 10-13% in our tests). Encrypting and compressing a link that never leaves the
machine only costs CPU on both ends too, so you can turn both off:

```toml
[servers]
lobby = { address = "127.0.0.1:19133", transport = "tcp", encryption = false, compression = false }
```

The server must listen on TCP: Dragonfly with gophertunnel's TCP network (`Network.Transport`
including `"tcp"`), on an address only Beacon can reach, such as `127.0.0.1`. `encryption` and
`compression` only work with `transport = "tcp"`. Players' own connections to Beacon are always
encrypted.

### Holding players while a server restarts

When a server goes down, Beacon normally moves its players to another server. With `hold`, they
stay on Beacon instead, in the world they were in, frozen in place with a title saying the server
is restarting, and are put back on the same server as soon as it is up again: no loading screen and
no trip through another server.

```toml
[servers]
kits = { address = "127.0.0.1:19135", transport = "tcp", hold = "90s" }
```

- A server that is not back within the hold time (at most `"10m"`) sends its players on as before.
- Only a server going down holds its players: the connection closing or breaking, or a kick
  message that says so ("restart", "server closed", "shutting down", Dragonfly's default). A server
  kicking or banning one player shows them the kick as before. If your server says something else
  when it stops, add it with `hold-reasons = ["see you soon"]` (parts of the message, any case).
- `hold-message = "Back in a moment..."` changes the title (default "Server restarting, hold on...").
- Held players still count as on that server (`/glist`, the Friends tab), and can use `/server` to
  go elsewhere while they wait.
- Players come back four at a time, so a full server does not log in all at once.
- If the server comes back with the player in another dimension than before, they are moved there
  with a normal dimension change.

### Leaving out a server's resource packs

Players get every server's resource packs when they join. To leave one server's packs out (say an
older copy of a server whose blocks the other servers' packs already draw), write it as a table:

```toml
[servers]
lobby = "127.0.0.1:19133"
old = { address = "127.0.0.1:19134", resource-packs = false }
```

Its blocks and items still work; they are drawn by the other servers' packs.

### Forwarding: who the player is

Your server only sees Beacon's login, so Beacon also tells it who the player really is. How depends
on the server, set per server with `forwarding`:

| Your server | `forwarding` | Also do | Players' real XUID |
|---|---|---|---|
| Dragonfly, Geyser | leave it out | nothing (Dragonfly can also [check Beacon's signature](docs/forwarding.md)) | yes |
| BDS with Endstone 0.12 or newer | `"endstone"` | [install Beacon's key](#endstone-012-and-newer) | yes |
| BDS with an older Endstone | leave it out | install [xuidforward](#older-endstone-xuidforward) | yes |
| BDS without Endstone | leave it out | nothing possible | no ([why](#bds-without-endstone)) |

#### Leaving it out: Beacon's signature

Beacon signs every login with the secret in `forwarding.secret`. A server that checks it (Dragonfly
with Beacon's Go package, xuidforward) refuses any login that didn't come through Beacon; one that
doesn't simply ignores it. Details and the format: [forwarding](docs/forwarding.md).

#### Endstone 0.12 and newer

Endstone 0.12 can trust a proxy's own key beside Microsoft's (`[proxies.endgate]` in
`endstone.toml`), so the server checks each login like any online one and keeps everything that
goes by XUID: ops, permissions, the allowlist and plugins. No plugin needed.

1. Set the server to `forwarding = "endstone"` in `beacon.toml` and start Beacon. It makes
   `endstone-forwarding.pem` in its folder.
2. Copy that file to the server as `keys/forwarding_key.pem`.
3. In the server's `server.properties`, set `online-mode=true`.
4. In the server's `endstone.toml`:

   ```toml
   [proxies.endgate]
   enabled = true
   forwarding-key-file = "keys/forwarding_key.pem"
   ```

The server now refuses any login that didn't come through Beacon (the message names endgate, the
proxy Endstone wrote this for; Beacon uses the same format) and sees players' own IP addresses.
Keep `endstone-forwarding.key`, next to Beacon, private: it is what signs the logins.

#### Older Endstone: xuidforward

Install [xuidforward](https://github.com/ezchr/xuidforward), an Endstone plugin by Beacon's author,
version 1.0.5 or newer, and give it the same `forwarding.secret` (its README shows how). Keep
`online-mode=false`.

#### BDS without Endstone

Plain BDS throws away the XUID of a login it didn't get from Microsoft. Beacon gives each player the
same stand-in ID every time, so their saves stay, but anything that goes by XUID (ops and
permissions by XUID, the allowlist) doesn't work, and a player who once joined the server directly
has separate saves there. If you need XUIDs without Endstone and don't need `/server` switching, use
[relayless mode](#without-server-switching-relayless-mode): players log in to BDS themselves.

## Without server switching (relayless mode)

If you don't need `/server` switching between servers, set (see
[Relay and relayless mode](docs/modes.md)):

```toml
mode = "relayless"
```

Beacon then lists your server on the Friends tab as usual, but hands each player straight to it
instead of carrying their connection, so it is not in the connection at all: players keep playing
through a Beacon restart, and Beacon's machine carries no game traffic. Your server keeps its own
Xbox check on (`online-mode=true`), needs nothing else, and must be reachable by players. It must
offer NetherNet (BDS 1.26.60+ does by default).

## What else it does

All on by default unless noted; details in [Features](docs/features.md).

- **Different content per server:** servers may have their own resource packs, blocks and items;
  Beacon merges and renumbers them, so players move between a vanilla server and a modded one.
- **Private command blocks:** players can't read the commands in command blocks and NPCs.
- **Datacenter block (opt-in):** turns away joins from hosting providers' IP ranges you list.
- **Bans and mutes:** `/ban`, `/banip` and `/mute` across all your servers, for offline players
  too, with an optional time.
- **Packet logs:** `debug Steve` writes what a player sends and gets to a file, for when something
  goes wrong in game. See [Debugging](docs/debugging.md).
- **Skins for Java players:** uploads Bedrock skins to GeyserMC's skin API, for servers with Java
  players.
- **HTTPS certificates (opt-in)** for joining by address: a free one from Let's Encrypt (`domain`)
  or your own (`tls-cert`, `tls-key`).
- **Split-screen:** a console player's split-screen guests join as players of their own and follow
  their host between servers.
- **Network-wide player count** in the server list.
- **Cross-server chat (opt-in):** your servers can share chat through the [control API](docs/control-api.md).

## The config file

`beacon.toml` explains every setting in comments. The main ones:

| Setting | Meaning |
|---|---|
| `mode` | `"relay"` (default): players go through Beacon and can switch servers. `"relayless"`: players connect straight to your one server, no switching. |
| `bind` | where players join by IP, e.g. `"0.0.0.0:19132"`. Empty: Friends tab only. |
| `motd` | the world name on the Friends tab and server list |
| `forwarding-secret-file` | the secret Beacon signs logins to your servers with; see [forwarding](docs/forwarding.md) |
| `[servers]` | your servers ([options](#server-options)), and `try`: the order players join in. If one can't be joined, the next is tried; if a player's server stops, they're moved to another one. |
| `[forced-hosts]` | players who join by a given address get their own order, e.g. `"pvp.example.com" = ["pvp"]` |
| `ping-passthrough` | what the server list takes from your first server: `"off"`, `"motd"`, `"players"` or `"all"` |
| `login-ratelimit`, `packets-per-second`, `sessions-per-ip` | limits under `[advanced]`: least milliseconds between joins from one IP, most packets per player per second (averaged over `packet-window` seconds, 7; `packet-burst` times it in one second is too many, 4), most players online from one IP; `0` means no limit |
| `[broadcast]` | the Friends-tab side: host name, admins, friend requests; see [Friends tab](docs/friends-tab.md) |
| `moderation` | under `[advanced]`: `false` leaves `/ban`, `/mute` and the rest to your servers, if you have your own ban plugin |
| `control-port` | a local HTTP API on 127.0.0.1 for your servers and scripts (player ping, cross-server chat, Friends-tab listing, invites); see [control API](docs/control-api.md) |

The environment variable `BEACON_FORWARDING_SECRET`, when set, is used instead of the secret file.

**Why Beacon needs an Xbox account:** Beacon asks you to sign in on start and keeps the sign-in in
`token.json`. It needs it for two things:

- **The Friends tab:** a world on the Friends tab is always hosted by an Xbox account; players see it
  once they are friends with that account. Minecraft lets at most **30 players** join one such
  world at once, and that can't be changed; for more, add hosting accounts (`beacon accounts add`),
  each one more world with 30 more places. See [Friends tab](docs/friends-tab.md).
- **Connecting to your servers:** Beacon connects to your servers for its players, and NetherNet
  servers (BDS 1.26.60+, Dragonfly, Geyser) refuse a connection that doesn't carry an identity from
  an Xbox account, even with `online-mode=false`. Your servers need no account themselves.

## Commands

These are for relay mode only: in [relayless mode](#without-server-switching-relayless-mode)
Beacon only puts your server on the Friends tab and has no in-game commands.

In game, for everyone: `/server [name]` and `/ping [player]` (your ping, or another player's; `@a`,
`@r`, `@s` and `@p` work too). Only the player who types `/ping` sees the answer.
In game, for Beacon's admins: `/glist`, `/send <player|all|current> <server>`, `/kick <player> [reason]`,
`/broadcast [on|off] [world]`, `/beacon reload`, and [bans and mutes](docs/features.md#bans-and-mutes):

```
/ban Steve 7d griefing
/banip Steve 30d
/mute Steve 1h spam
/unban Steve
```

Admins are the `admins` in `beacon.toml`, and anyone you add with `admin add <player>` in the console.

In Beacon's console:

| Command | |
|---|---|
| `end` | stop Beacon |
| `glist` | who is on which server |
| `send <player\|all> <server>` | move players |
| `broadcast [on\|off] [world]` | show or hide worlds on the Friends tab: every world, or one by name; alone, lists them |
| `invites start\|stop\|status` | Xbox invites to the players `beacon scrape` found |
| `friendadd <server>\|stop\|status` | friend requests to the players seen on a server |
| `reload` | reread `beacon.toml` without a restart, and say what still needs one |
| `ban`, `banip`, `mute`, `unban`, `unbanip`, `unmute`, `banlist`, `mutelist` | as in game, without the slash |
| `admin add\|remove <player>`, `admin list` | make or unmake a Beacon admin |
| `debug` | log packets to a file; type it alone for how, or see [Debugging](docs/debugging.md) |
| `help` | list the commands |

Running as a service, with no console? `ban`, `admin` and `debug` work through the
[control API](docs/control-api.md) too.

From a shell:

| Command | |
|---|---|
| `beacon accounts list` | the Xbox accounts hosting the world, with their friend counts |
| `beacon accounts add <name>` | add another hosting account (30 more join slots) |
| `beacon accounts remove <name>` | remove one |
| `beacon scrape <server>` | collect the players seen on a server or friends' worlds, to invite them; see [scraper](docs/scraper.md) |
| `beacon worlds` | the Friends-tab worlds the scraper account's friends have open |

## Running it as a service (Linux)

```ini
# /etc/systemd/system/beacon.service
[Unit]
Description=Beacon Proxy
After=network-online.target

[Service]
WorkingDirectory=/opt/beacon
ExecStart=/opt/beacon/beacon
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

Sign in once by running `./beacon` by hand in that folder, then
`systemctl enable --now beacon`.

## More

- [Relay and relayless mode](docs/modes.md): what each does and when to pick it.
- [Features](docs/features.md): everything else Beacon does, and how to set it up.
- [Debugging](docs/debugging.md): see the packets between a player and your servers.
- [Player-info forwarding](docs/forwarding.md): how servers know a login came from Beacon, and
  the claim format for writing a check for another server.
- [Friends tab](docs/friends-tab.md): accounts, friend limits and invites.
- [Control API](docs/control-api.md): the local HTTP routes for your servers and scripts.
- [Scraper](docs/scraper.md): `beacon scrape`, which collects players to invite.
- [Building from source](docs/building.md)

## Credits

- [NetherConnect](https://github.com/GameParrot/netherconnect): the NetherNet-to-RakNet relay.
- [MCXboxBroadcast](https://github.com/MCXboxBroadcast/Broadcaster): broadcasting a world to the
  Friends tab.

Beacon's configuration, commands and player-info forwarding are modelled on
[Velocity](https://github.com/PaperMC/Velocity).

## License

MIT, see [LICENSE](LICENSE).
