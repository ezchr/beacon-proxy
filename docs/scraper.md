# Scraper

`beacon scrape` joins a Bedrock server, or a friend's world on the Friends tab, as a normal
signed-in player and writes down the players it sees there. Beacon can then send them Xbox invites
or friend requests. It is optional; most setups don't need it.

## Setting it up

The scraper uses **its own Xbox account**, never the one that hosts your world, so the hosting
account only ever hosts.

1. Open a second terminal **in Beacon's folder** (the one with `beacon.toml`).
2. Run the scraper with a target:

   ```
   ./beacon scrape play.example.com:19132     # a server, by address
   ./beacon scrape -friend SomeGamertag       # the world a friend has open on the Friends tab
   ./beacon scrape                            # every world the scraper account's friends have open
   ```

3. The first time, it asks you to sign in: use the scraper's Xbox account. The sign-in is kept in
   `tokens/scraper.json`.

It keeps running until you stop it (Ctrl+C), reconnects when it is dropped, and never writes the
same player twice. Players from servers go to `server_players.txt`, players from friends' worlds to
`world_players.txt`, next to `beacon.toml`, where Beacon reads them.

`./beacon worlds` lists the worlds the scraper account's friends have open right now, busiest
first, to pick a `-friend` target.

Other options: `-experience <id>` (a featured server, by its experience id), `-reconnect-delay`,
`-advertise` (show the joined world to the scraper account's own friends), `-no-friend-accept`,
`-token <file>`. `./beacon scrape -h` lists them all.

## Using what it found

In Beacon's console:

| Command | |
|---|---|
| `invites start` | send Xbox invites to the found players, one at a time, over and over |
| `invites stop`, `invites status` | |
| `friendadd play.example.com:19132` | send friend requests, from the hosting account, to the players seen on that server or world |
| `friendadd stop`, `friendadd status` | |

The same actions are on the [control API](control-api.md). Players who already joined, or who are
already friends, are skipped.

## Be careful

Joining other people's servers to collect players, and sending invites, can break those servers'
rules and Xbox Live's terms, and accounts that send many invites get rate-limited or restricted.
Use it on servers you have permission for, and send invites slowly.
