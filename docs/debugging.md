# Debugging with packet logs

Something looks wrong in game and you want to see what really happened: a player gets kicked, an
item doesn't show, a menu closes. Beacon can write down the packets between a player and your
servers, so you can see what was sent and by whom.

## Quick start

1. Start logging a player:
   ```
   debug Steve
   ```
2. Have them do what goes wrong.
3. Stop and open the file it names:
   ```
   debug off
   ```

Each line is one packet, with its time, the player, the server, who sent it, its name, its size and
what is in it:

```
21:04:11.203  Steve    survival client Text            23 B  &{TextType:1 SourceName:Steve Message:hello ...
21:04:11.240  Steve    survival server MobEquipment    61 B  &{EntityRuntimeID:12 NewItem:...
```

`client` is the player sending to the server, `server` the other way.

The first time a player shows up in a file, it says who they are, which helps when something only
happens on one device or version. It also gives everything from their login, and saves their skin as
a picture beside the log (`packets-...-Steve.png`):

```
# player Steve xuid=2535400000000000 ip=203.0.113.7 device=Android model="SM-G991B" version=1.26.60
```

## Commands

| Command | Logs |
|---|---|
| `debug` | nothing: says what is running, and how to use it |
| `debug <player>` | what one player sends and gets, with contents |
| `debug <player> Text MobEquipment` | only those packets |
| `debug all` | every packet of everyone, names and sizes only |
| `debug all Text` | everyone, those packets, with contents |
| `debug off` | stops, and names the file |

Every packet is logged unless you filter it. A gamertag with a space goes in quotes:
`debug "Some Player"`. Packet names don't care about capitals.

## Filters

Add any of these after the player, in any mix:

| Filter | Logs |
|---|---|
| `Text MobEquipment` | only these packets |
| `-MovePlayer` | everything but this packet |
| `quiet` | everything but movement, chunks and what else goes by many times a second |
| `client` or `server` | only what that side sends |
| `on:survival` | only on this server |
| `has:diamond` | only packets with this word in them |

For example, `debug Steve quiet on:survival` or `debug all Text has:hacks`.

Start with `debug Steve quiet`. Once you can see which packet matters, name it, and the file gets much
shorter.

## Finding the problem

Note the time it went wrong, then look at the lines just before it. The last packets before a kick
are usually the cause.

| What you see | Try | Look for |
|---|---|---|
| A player is kicked or disconnected | `debug Steve quiet` | the last lines, and a `Disconnect` packet: its message says why |
| Chat is missing or wrong | `debug Steve Text` | `client` lines are what they typed, `server` lines what came back |
| A command does nothing | `debug Steve CommandRequest CommandOutput` | whether it reached the server, and the answer |
| A menu or form closes | `debug Steve ContainerOpen ContainerClose ModalFormRequest` | what closed it, and which side sent that |
| An item or armour looks wrong | `debug Steve MobEquipment MobArmourEquipment InventorySlot` | the item the server really sent |
| A player can't move between servers | `debug Steve Transfer Disconnect ResourcePacksInfo` | where it stops |
| Something is lagging | `debug all` | which packets show up the most |

A packet that a `client` line sends but no `server` line answers usually means the server ignored it.
If Beacon logged the right thing and the player still sees it wrong, the problem is in the client or
its resource packs, not in the relay.

To search a big file:

```
grep Disconnect debug/packets-2026-11-06_21-04-11.log
grep -c MobEquipment debug/packets-2026-11-06_21-04-11.log
```

The contents are the packet's fields as Beacon read them. A long packet is cut at 2000 characters and
ends with `...cut`.

## Where to type it

In Beacon's console. If Beacon runs as a service with no console, use the [control API](control-api.md):

```
curl '127.0.0.1:7777/packetdebug?cmd=Steve'
curl '127.0.0.1:7777/packetdebug?cmd=off'
```

Use your own `control-port` in place of 7777. For more than one word, use `+` for spaces, and `%22`
for the quotes around a name with a space:

```
curl '127.0.0.1:7777/packetdebug?cmd=Steve+Text'
curl '127.0.0.1:7777/packetdebug?cmd=%22Some+Player%22+Text'
```

## Where the logs are

In `debug/` beside `beacon.toml`, one file per run, such as `packets-2026-11-06_21-04-11.log`. They stay
after a restart. The newest 10 are kept.

A run stops by itself after 5 minutes or 50 MB, so a forgotten one can't fill your disk. Starting a
new run stops the one before it. While nothing is running it costs nothing.

## Before you share a log

It holds players' IP addresses, devices, XUIDs and login details, and what the packets hold, which can
be private chat and positions. Only you can read the file on the server, but remove all that before
posting it anywhere.

Beacon keeps secrets out, so a log is safe to keep on your server:

- Logins and the encryption handshake show only that they happened, so no Xbox tokens. What a login said is logged, read out of it.
- Purchase receipts are hidden.
- Resource pack decryption keys are hidden.
- `/login`, `/register` and similar commands show the command, not the password.
- Text typed into forms is hidden; the buttons and options chosen are kept.

Chat is logged as it was sent, so a player who types a password into chat will have it in the log.

It only sees players relayed through Beacon, so it needs relay mode and doesn't show Java players.
