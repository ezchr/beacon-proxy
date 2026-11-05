# The Friends tab

Beacon lists your server on the Friends tab of the players' Minecraft, as if an Xbox account were
hosting a world. Players see it once they are friends with that account, on any device: phones,
consoles, Windows.

## The hosting account

Beacon asks you to sign in the first time it runs; that account hosts the world. Use one made for
this, not your own. Its token is kept in `token.json`: keep that file private, it is a sign-in.

Players add the account as a friend in Minecraft or the Xbox app. Beacon accepts friend requests on
its own (`accept-friends = true` under `[broadcast]`), and once they are friends the world shows up.

## Limits, and more accounts

Xbox Live limits each account to **1000 friends** and each listed world to **30 players**. To go
past either, add more accounts; each lists the same servers as one more world:

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

## Settings

Under `[broadcast]` in `beacon.toml`:

| Setting | Meaning |
|---|---|
| `host-name` | the name shown as the world's host; default: the `motd` |
| `admins` | gamertags or XUIDs that may use `/glist`, `/send` and `/broadcast` |
| `accept-friends` | accept friend requests automatically |
| `listing-toggle` | lets admins hide the world from the Friends tab with `/broadcast off`; players already in stay, and joining by IP still works |

## Inviting players

Beacon can send Xbox invites to the world: to the players [`beacon scrape`](scraper.md) found
(`invites start` in the console), or to the hosting account's own friends through the
[control API](control-api.md) (`/invites/friends/start`).

Send invites sparingly: Xbox Live rate-limits them, and accounts that send too many can be
restricted.
