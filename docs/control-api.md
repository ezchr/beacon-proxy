# Control API

Beacon has a small HTTP API for your own servers and scripts on the same machine: plugins ask it
for a player's ping or send chat across servers, and you can switch things on and off with `curl`.

It listens on `127.0.0.1` only, on `control-port` (default 7777, under `[advanced]` in
`beacon.toml`). When `bind` is itself on `127.0.0.1` (a reverse proxy such as nginx faces the
players), the API shares `bind`'s address instead. It is never on a public address: anyone who could
reach it could switch your world off the Friends tab or send invites from your account.

```
curl 127.0.0.1:7777/listing/status
```

## Routes

| Route | What it does |
|---|---|
| `/ping?xuid=<xuid>` | a connected player's real round trip in ms (their device to Beacon, plus Beacon to the server), for a `/ping` command on your server. `not connected` when Beacon doesn't have them. |
| `/listing/status`, `/listing/on`, `/listing/off` | show or hide the worlds on the Friends tab, like `/broadcast`; add `?world=NAME` for one world. Needs `listing-toggle = true` |
| `/close?message=<text>`, `/open` | close the network: everyone but Beacon's admins is disconnected with the message, and new joins (Friends tab or address) are turned away with it until `/open`. Stays closed across restarts |
| `/chat/send`, `/chat/poll` | chat across your servers, when `cross-server-chat = true`: a server posts its players' messages and polls for the others' |
| `/invites/start`, `/invites/stop`, `/invites/status` | send Xbox invites to the players [`beacon scrape`](scraper.md) found (and any in `invite_queue.txt`); the console's `invites` command does the same |
| `/invites/friends/start`, `/invites/friends/stop`, `/invites/friends/status` | invite the hosting account's own friends |
| `/friends/add/start?from=<server or world>`, `/friends/add/stop`, `/friends/add/status` | send friend requests to the players recorded from a server or world; the console's `friendadd` does the same |
| `/broadcast/<name>/invites/...` | the invite routes for an extra hosting account, if it has `invites = true` |
| `/moderation?cmd=<command>` | a moderation or `admin` command, as in the console; see below. Add `&by=<name>` to run it for someone a Java proxy let use it: their name goes on what they do, and they can't ban or mute Beacon's admins |
| `/moderation/java/login?uuid=&name=&ip=` | for a Java proxy: notes a Java player logging in and answers `{"banned", "message"}` |
| `/moderation/java/state` | for a Java proxy: POST a JSON list of the Java players on, `[{"uuid", "name", "ip", "ping", "server"}]`; answers the ones banned (`ban`, the message to remove them with) or muted (`mute`, the time left), by UUID. `ping` (ms) and `server` put them in `/ping` and `/glist` |
| `/moderation/java/mutes` | the muted Java players, `{"<uuid>": "<time left>"}`: a Java proxy can't stop a signed chat message, so your servers drop a muted Java player's chat and `/msg` themselves |
| `/ping/run?args=<name or @a/@r>` | `/ping` as Beacon answers it, for a Java proxy's own `/ping`; `404` when no player of that name is on |
| `/broadcast/run?args=<on\|off\|status [world]>&by=<name>` | `/broadcast` as Beacon answers it, for a Java proxy's own `/broadcast`; `/broadcast/worlds` lists the world names |
| `/names` | everyone on the network, Bedrock and reported Java players, one name a line (for a Java proxy's command suggestions) |
| `/glist` | the players on every server, Bedrock and the Java ones a Java proxy reported, as `/glist` shows them |
| `/packetdebug?cmd=<what>` | packet logging, like the `debug` console command; see [Debugging](debugging.md) |
| `/debug/pprof/...` | Go's profiler, only when Beacon runs with `-debug` |

Moderation commands go in `cmd`, with `+` for spaces and `%22` for the quotes around a name with a
space:

```
curl '127.0.0.1:7777/moderation?cmd=ban+Steve+7d+griefing'
curl '127.0.0.1:7777/moderation?cmd=mute+%22Some+Player%22+1h'
curl '127.0.0.1:7777/moderation?cmd=admin+add+Steve'
```

Send invites and friend requests sparingly: Xbox Live rate-limits them, and accounts that send too
many can be restricted.
