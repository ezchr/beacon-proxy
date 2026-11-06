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
| `/listing/status`, `/listing/on`, `/listing/off` | show or hide the world on the Friends tab, like `/broadcast on\|off` (needs `listing-toggle = true`) |
| `/chat/send`, `/chat/poll` | chat across your servers (`/chat/send` also takes `player`, `xuid` and `message`, for Discord), when `cross-server-chat = true`: a server posts its players' messages and polls for the others' |
| `/invites/start`, `/invites/stop`, `/invites/status` | send Xbox invites to the players [`beacon scrape`](scraper.md) found (and any in `invite_queue.txt`); the console's `invites` command does the same |
| `/invites/friends/start`, `/invites/friends/stop`, `/invites/friends/status` | invite the hosting account's own friends |
| `/friends/add/start?from=<server or world>`, `/friends/add/stop`, `/friends/add/status` | send friend requests to the players recorded from a server or world; the console's `friendadd` does the same |
| `/broadcast/<name>/invites/...` | the invite routes for an extra hosting account, if it has `invites = true` |
| `/moderation?cmd=<command>` | a moderation command, like `ban+Steve+7d`, as in the console |
| `/packetdebug?cmd=<what>` | packet logging, like the `debug` console command; see [Debugging](debugging.md) |
| `/debug/pprof/...` | Go's profiler, only when Beacon runs with `-debug` |

Send invites and friend requests sparingly: Xbox Live rate-limits them, and accounts that send too
many can be restricted.
