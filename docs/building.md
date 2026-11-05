# Building from source

Releases include ready-made binaries; you only need this to change Beacon.

Needs Go 1.26 or newer.

```
git clone https://github.com/ezchr/beacon-proxy
cd beacon-proxy
go build -o beacon .
go test ./...
```

For other systems set `GOOS` and `GOARCH`, for example `GOOS=windows GOARCH=amd64 go build -o
beacon.exe .`

`tools/` holds development tools; see [tools/README.md](../tools/README.md).
