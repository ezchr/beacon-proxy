# The Friends tab

Beacon lists your server on the Friends tab of the players' Minecraft, as if an Xbox account were
hosting a world. Players see it once they are friends with that account, on any device: phones,
consoles, Windows.

## The hosting account

Beacon asks you to sign in the first time it runs; that account hosts the world. Use one made for
this, not your own. Its token is kept in `token.json`: keep that file private, it is a sign-in.

Players add the account as a friend in Minecraft or the Xbox app. Beacon accepts friend requests on
its own (`accept-friends = true` under `[broadcast]`), and once they are friends the world shows up.

## 30 players per world, and more accounts

A world on the Friends tab holds at most **30 players** at once. That limit is built into Minecraft
and Xbox Live: no server, proxy or setting can raise it. Once 30 players have joined a world,
nobody else can connect to it, even if its max shows more (`show-max-players` can be set higher,
as in Minecraft itself, but it only changes the number shown). Players who join by IP address
don't count.

To let more players in from the Friends tab, list more worlds: each extra Xbox account broadcasts
your servers as one more world with its own 30 places. Players still end up on the same servers
together, whichever world they joined from. Extra accounts also help with the other limit: an
account can have at most **1000 friends**.

Add one with:

```
beacon accounts add alt1
```

It asks you to sign in with another Xbox account, makes it and the main account friends, and adds
it to `beacon.toml`. Restart Beacon to start its broadcast. See them all with:

```
beacon accounts list
```

which shows each account's friend count and warns when one is nearly full. `beacon accounts remove
alt1` takes one out again (its sign-in is kept as `tokens/alt1.json.removed`).

## One world per server

With [several servers](features.md#moving-between-servers-relay-mode), each world can lead to its own
server: set `server` on it to one of the names under `[servers]`. Without it, a world leads to the
first server in `try`, as before.
Players who join through that world land there first, and can still move with `/server`. Players
who join through a friend in that world land there too, and players who join by IP address land
on the first server in `try`.

```toml
[broadcast]
host-name = "My Survival"
server = "survival"

[[broadcast.extra]]
name = "minigames"
token-file = "tokens/minigames.json"
host-name = "My Minigames"
server = "minigames"
```

A player stays in the world they joined until they leave, wherever `/server` takes them, so their
friends keep seeing them there.

## Settings

Under `[broadcast]` in `beacon.toml`:

| Setting | Meaning |
|---|---|
| `host-name` | the name shown as the world's host; default: the `motd` |
| `server` | the server players joining through this world land on first; default: the first in `try` |
| `admins` | gamertags or XUIDs that may use `/glist`, `/send` and `/broadcast` |
| `accept-friends` | accept friend requests automatically |
| `listing-toggle` | lets admins hide the world from the Friends tab with `/broadcast off`; players already in stay, and joining by IP still works |

## Inviting players

Beacon can send Xbox invites to the world: to the players [`beacon scrape`](scraper.md) found
(`invites start` in the console), or to the hosting account's own friends through the
[control API](control-api.md) (`/invites/friends/start`).

Send invites sparingly: Xbox Live rate-limits them, and accounts that send too many can be
restricted.
