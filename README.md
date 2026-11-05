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
   and enter the code. **Use an Xbox account made for this**, not your own: the world is shown as
   hosted by it, and everyone who wants to see it on the Friends tab has to be its friend.

Players now find the world on the Friends tab once they friend the Beacon account, or join by your
IP and port 19132.

## Setting up your servers

In relay mode (the default) Beacon logs into your servers for the players, so each server must
accept Beacon's login: turn its own Xbox check off. Beacon has already checked the player.

| Server | Setting |
|---|---|
| BDS | `online-mode=false` in `server.properties`, plus [xuidforward](https://github.com/ezchr/xuidforward) (below) |
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

On the server's machine, let only Beacon in:

```
ufw allow from 203.0.113.5 to any port 19133
```

If you can add code to the server, also have it check Beacon's signature on each login (see
[forwarding](docs/forwarding.md)); then even a connection that gets past the firewall is refused.

### BDS keeps player data with xuidforward

BDS throws away the Xbox ID (XUID) of a login like Beacon's, so without help every player starts
with fresh, empty data each time they join. Install
[xuidforward](https://github.com/ezchr/xuidforward), an Endstone plugin by Beacon's author, version
1.0.5 or newer, and give it the same `forwarding.secret` (its README shows how). Dragonfly and Geyser
keep the XUID on their own, and relayless mode needs nothing.

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
- **Game versions:** a server one version behind still takes players on the new version.
- **Private command blocks:** players can't read the commands in command blocks and NPCs.
- **Datacenter block (opt-in):** turns away joins from hosting providers' IP ranges you list.
- **Skins for Java players:** uploads Bedrock skins to GeyserMC's skin API, for servers with Java
  players.
- **HTTPS certificates (opt-in)** for joining by address: a free one from Let's Encrypt (`domain`)
  or your own (`tls-cert`, `tls-key`).
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
| `[servers]` | your servers, and `try`: where players go first |
| `[broadcast]` | the Friends-tab side: host name, admins, friend requests; see [Friends tab](docs/friends-tab.md) |
| `control-port` | a local HTTP API on 127.0.0.1 for your servers and scripts (player ping, cross-server chat, Friends-tab listing, invites); see [control API](docs/control-api.md) |

The environment variable `BEACON_FORWARDING_SECRET`, when set, is used instead of the secret file.

**Why Beacon needs an Xbox account:** Beacon asks you to sign in on start and keeps the sign-in in
`token.json`. It needs it for two things:

- **The Friends tab:** a world on the Friends tab is always hosted by an Xbox account; players see it
  once they are friends with that account.
- **Connecting to your servers:** Beacon connects to your servers for its players, and NetherNet
  servers (BDS 1.26.60+, Dragonfly, Geyser) refuse a connection that doesn't carry an identity from
  an Xbox account, even with `online-mode=false`. Your servers need no account themselves.

## Commands

In game, for everyone: `/server [name]`.
In game, for the `admins` in `beacon.toml`: `/glist`, `/send <player|all|current> <server>`,
`/broadcast [on|off]`.

In Beacon's console:

| Command | |
|---|---|
| `end` | stop Beacon |
| `glist` | who is on which server |
| `send <player\|all> <server>` | move players |
| `broadcast [on\|off]` | show or hide the world on the Friends tab |
| `invites start\|stop\|status` | Xbox invites to the players `beacon scrape` found |
| `friendadd <server>\|stop\|status` | friend requests to the players seen on a server |
| `help` | list the commands |

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
