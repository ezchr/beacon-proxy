# beacon.toml reference

Every setting Beacon reads, by section. Beacon writes `beacon.toml` on first start: the settings most
networks change are set there, the rest are listed as comments. Leave out anything you don't need;
the default applies.

## Top level

| Setting | Values | Default | What it does |
|---|---|---|---|
| `config-version` | `"1.0"` | `"1.0"` | file format version; leave it |
| `mode` | `"relay"` \| `"relayless"` | `"relay"` | relay: players go through Beacon and can `/server`. relayless: Beacon only gets them onto your one server ([modes](modes.md)) |
| `bind` | `"host:port"` | `""` | where players join by address. Empty: Friends tab only |
| `domain` | domain name | `""` | free Let's Encrypt certificate for joining by address (needs `bind`) |
| `tls-cert`, `tls-key` | file paths | `""` | your own certificate instead of `domain` |
| `forwarding-secret-file` | path | `"forwarding.secret"` | the secret Beacon signs logins with ([forwarding](forwarding.md)). `BEACON_FORWARDING_SECRET` overrides it |
| `motd` | text | `"Beacon"` | name on the server list and the Friends tab |
| `max-players` | number | `0` | most players online at once; more are told it's full. Admins always get in. `0`: no cap |
| `show-max-players` | number | `20` | the max shown on the server list and the Friends tab; limits nobody |
| `ping-passthrough` | `"off"` \| `"motd"` \| `"players"` \| `"all"` | `"off"` | what the server list copies from your first server |

## [servers]

A server is `name = "host:port"`, or a table with options:
`survival = { address = "127.0.0.1:19134", forwarding = "endstone", hold = "90s" }`.

`try = ["lobby", ...]` is the order players join in. If one can't be joined the next is tried, and a
player whose server stops is moved to the next one. Default: the first server.

| Option | Values | Default | What it does |
|---|---|---|---|
| `address` | `"host:port"` | required | where Beacon reaches the server |
| `transport` | `"nethernet"` \| `"raknet"` \| `"tcp"` | `"nethernet"` | raknet: a server that only speaks RakNet. tcp: a Dragonfly server on the same machine listening on TCP |
| `forwarding` | left out \| `"endstone"` | left out | how the server learns the player's XUID. Left out: Beacon signs it into the login (Dragonfly, Geyser, xuidforward). `"endstone"`: Endstone 0.12+ ([setup](../README.md#endstone-012-and-newer)) |
| `resource-packs` | `true` \| `false` \| `"on-join"` | `true` | true: downloaded on joining the network. false: never sent (its custom blocks show without textures unless another server sends the same pack). `"on-join"`: downloaded on joining this server, together with its custom blocks and items, which other players then never get; players rejoin once (set `rejoin-address`) |
| `hold` | `"90s"` (5s to 10m) | off | keep the server's players on Beacon while it restarts, instead of moving them |
| `hold-message` | text | `"Server restarting, hold on..."` | title held players see |
| `hold-reasons` | `["text", ...]` | built in | more kick messages that mean the server is restarting |
| `allowed` | `["xuid", ...]` | everyone | only these players may join; others are told it's closed and don't see it in `/server` |
| `encryption` | `false` | `true` | tcp only: don't encrypt the link |
| `compression` | `false` | `true` | tcp only: don't compress the link |
| `protocols` | `[2193, ...]` | all | game versions (protocol numbers) the server takes |
| `native` | `[2225, ...]` | none | newer versions the server speaks itself; players on other versions are translated |
| `client-side-generation` | `true` | `false` | BDS with client-side chunk generation: players who join to go there build far chunks themselves |

## [forced-hosts]

`"pvp.example.com" = ["pvp"]`: players who join by that address get this order instead of `try`.

## [broadcast]

The Friends-tab world, hosted by the account in `token.json` ([Friends tab](friends-tab.md)).

| Setting | Values | Default | What it does |
|---|---|---|---|
| `world-name` | text | `motd` | world title on the Friends tab |
| `host-name` | text | `motd` | host name shown under it |
| `name` | text | `"main"` | this world's name in `/broadcast` and the control API |
| `admins` | `["xuid", ...]` | `[]` | Beacon admins. Beacon writes each one's gamertag after it |
| `moderators` | `["xuid", ...]` | `[]` | `/kick`, `/unmute`, `/mutelist`, `/banlist`, and `/ban` and `/mute` for up to 1 day |
| `protected` | `["Name*", ...]` | `[]` | names staff can't ban, mute or kick (`*` matches anything) |
| `allowed-xuids` | `["xuid", ...]` | everyone | only these players may join the network |
| `accept-friends` | `true` \| `false` | `true` | accept friend requests automatically |
| `listing-toggle` | `true` \| `false` | `true` | admins may hide the world with `/broadcast off` |
| `player-count` | `"world"` \| `"server"` \| `"network"` | `"world"` | count the world shows: players who joined through it, players on its server, or everyone |
| `server` | server name | `try`'s first | where this world's players join first |
| `update-interval-seconds` | number | `30` | how often the world on the Friends tab is refreshed |
| `token-file` | | | ignored: the main world's account is always `token.json` |

## [[broadcast.extra]]

Another world on another Xbox account. `beacon accounts add <name>` signs it in and writes the section.

| Setting | Values | Default | What it does |
|---|---|---|---|
| `name` | text | `extra-N` | its name in `/broadcast` and the control API |
| `token-file` | path | required | its account's sign-in, written by `beacon accounts add` |
| `world-name` | text | `motd` | world title |
| `host-name` | text | `[broadcast]`'s | host name |
| `server` | server name | `try`'s first | where its players join first |
| `player-count` | as in `[broadcast]` | `[broadcast]`'s | count it shows |
| `accept-friends` | `true` \| `false` | `true` | accept friend requests |
| `invites` | `true` \| `false` | `false` | invite routes for this world on the control port ([control API](control-api.md)) |
| `protocol`, `version` | `2193`, `"1.26.30"` | the main world's | serve another game version from this world |

## [advanced]

| Setting | Values | Default | What it does |
|---|---|---|---|
| `control-port` | number | `7777` | local HTTP API on 127.0.0.1 ([control API](control-api.md)) |
| `cross-server-chat` | `true` \| `false` | `false` | your servers share chat through the control API |
| `moderation` | `true` \| `false` | `true` | Beacon's `/ban`, `/mute`, `/kick`. false: leave them to your servers |
| `kick` | `"network"` \| `"server"` | `"network"` | what `/kick` does: disconnect, or send to another server |
| `rejoin-address` | `"host:port"` | `""` | where players are sent to rejoin (`"on-join"` packs, changed packs). Empty: they're asked to rejoin |
| `login-ratelimit` | milliseconds | `0` | least time between joins from one IP. `0`: off |
| `packets-per-second` | number | `0` | most packets per player per second, averaged over `packet-window`. `0`: off |
| `packet-window` | seconds | `7` | the window `packets-per-second` is averaged over |
| `packet-burst` | number | `4` | one second over this many times `packets-per-second` is too many. `-1`: no burst check |
| `sessions-per-ip` | number | `0` | most players online from one IP. `0`: off |
| `fake-player-count` | number | `0` | Friends-tab count shown instead of the real one. `0`: off |
| `fake-player-drift` | `{ min, max, max-step }` | off | Friends-tab count that wanders between min and max, by at most max-step per update. `max = 0`: off |
| `compression` | `"snappy"` \| `"flate"` \| `"none"` | `"snappy"` | compression with players |
| `compression-threshold` | bytes | `256` | smaller packets aren't compressed |
| `protocol`, `version` | `2193`, `"1.26.30"` | this build's | game version advertised |
| `network-dir` | path | `"network"` | cache of your servers' content and packs |
| `acme-http-address` | `"host:port"` | `":80"` | where Let's Encrypt checks your domain |
| `disable-client-encryption` | `true` \| `false` | `false` | skip the encryption handshake with players. Only if a client version can't log in |
