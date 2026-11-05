# Migrating from nether2rak

Beacon Proxy is the new name of nether2rak. Your setup carries over:

1. Replace the `nether2rak-unified` binary with `beacon` in the same folder.
2. Start it. It finds your `config.json`, writes the same settings to `beacon.toml`, and renames the
   old file to `config.json.migrated`. Your `token.json` and other token files are used as they are.
3. Read the new `beacon.toml` once. Beacon now signs the logins it sends your servers (a new
   `forwarding.secret`); servers that don't check it ignore it. See [forwarding](forwarding.md).

What changed:

| nether2rak | Beacon |
|---|---|
| `config.json` | `beacon.toml` |
| `backend_transport: "nethernet"` + `servers` | `mode = "relay"` + `[servers]` |
| `backend_transport: "nethernet-norelay"` | `mode = "relayless"` |
| `backend_transport: "raknet"` | `mode = "relay"`, server with `transport = "raknet"` |
| `relay_direct_ip_listen_address` | `bind` |
| `world_name` / `host_name` | `motd` / `[broadcast] host-name` |
| `extra_broadcasts` | `[[broadcast.extra]]`, or `beacon accounts add` |
| `-login token2.json` | `beacon accounts add <name>` (`-login` still works) |
| `N2R_*` environment variables | `BEACON_*` (the old names still work) |
| Go module `github.com/ezchr/nether2rak` | `github.com/ezchr/beacon-proxy` |
