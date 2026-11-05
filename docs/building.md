# Building from source

Releases include ready-made binaries; you only need this to change Beacon.

Needs Go 1.26 or newer.

```
git clone https://github.com/ezchr/beacon-proxy
cd beacon-proxy
go build -o beacon ./cmd/beacon
go test ./...
```

For other systems set `GOOS` and `GOARCH`, for example
`GOOS=windows GOARCH=amd64 go build -o beacon.exe ./cmd/beacon`

## Layout

| Folder | |
|---|---|
| `cmd/beacon` | the beacon program: startup, `beacon.toml`, console, commands, Friends-tab broadcasting, invites, the control API |
| `forwarding` | the forwarding claim and the check servers run on it; the one package meant for other projects |
| `internal/bridge` | the relay: client and server connections, server switching, joining by address |
| `internal/proxy` | Minecraft login and packet handling between a player and a server |
| `internal/xbl`, `internal/session` | Xbox Live: sign-in, sessions, friends, invites |
| `internal/signaling`, `internal/messaging` | NetherNet signaling over Xbox Live |
| `internal/scraper`, `internal/worlds` | `beacon scrape` and `beacon worlds` |
| `internal/invitequeue` | the lists the scraper writes and invites read |
| `docs` | these pages |

`tools/` holds development tools; see [tools/README.md](../tools/README.md).
