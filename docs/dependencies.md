# Dependency upgrade notes

Checked 2026-11-13 against the Go module proxy and upstream sources. Versions
are pinned in `go.mod` and verified by `go.sum`.

## Direct dependencies

| Dependency | Previous | Updated |
| --- | --- | --- |
| Go minimum | 1.27.0 | 1.27.4 |
| tailscale.com | v1.102.3 | v1.104.7 |
| github.com/tailscale/wireguard-go | 20260715223240-2e01ba5b00f0 | 20261106233718-9e0ab45c16ca |
| github.com/godbus/dbus/v5 | v5.2.2 | v5.2.4 |
| github.com/miekg/dns | v1.1.58 | v1.1.73 |
| github.com/vishvananda/netlink | v1.3.1 | unchanged (latest) |
| golang.org/x/sys | v0.47.0 | v0.50.0 |
| YAML | gopkg.in/yaml.v3 v3.0.1 | go.yaml.in/yaml/v3 v3.0.6 |
| modernc.org/sqlite | v1.34.5 | v1.62.0 |

Transitive packages used by the project were also updated, with the compatibility
exceptions below. This does not independently upgrade every unused package in
Tailscale's module graph or change the Nix package's prebuilt release selection.

## Required changes and compatibility exceptions

- **Tailscale:** v1.104.7 is the latest stable release checked. Its Go minimum is
  1.27.4, so the project's `go` directive now matches. The release addresses TLS
  ClientHello compatibility with middleboxes; v1.104.5 also fixes tsnet
  connectivity stalls on lossy networks. `tsnet.Server.Tun` and the LocalClient
  APIs used here remain available.
- **WireGuard custom TUN:** the version required by Tailscale changes `Read`
  from `Read(bufs [][]byte, sizes []int, offset int)` to
  `Read(slab []byte, packets []tun.ReadPacket)`. Both the channel-backed profile
  TUN and the host TUN adapter now use slab-relative packet offsets and reserve
  `tun.ReadPacketSpacing` before, between, and after packets. The host adapter
  drains valid packets before reporting a simultaneous read error. Regression
  tests cover packet ownership, metadata, buffer limits, closure, and batches.
- **gVisor:** retain Tailscale v1.104.7's exact tested revision,
  `v0.0.0-20260915211658-a6f909f08a72`. An attempted update to latest,
  `v0.0.0-20261113212324-d99dee3baa1b`, fails ordinary Go builds with mixed
  package declarations in `pkg/tcpip/stack` and `pkg/refs`. Do not blindly
  upgrade this dependency separately from Tailscale.
- **SQLite libc:** retain `modernc.org/libc v1.80.1`, the version required by
  SQLite v1.62.0, rather than independently selecting latest v1.81.0. SQLite's
  documentation warns that its libc dependency is fragile and should match its
  own `go.mod`. Existing state persistence/recovery tests pass; no application
  schema migration was required.
- **YAML:** migrate the import to the YAML organization's maintained v3 fork.
  Existing strict-config parsing and serialization tests pass unchanged.

## Verification

Passed with Go 1.27.4 on Linux amd64:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go test -tags=integration ./...` (real-tailnet test skipped without auth keys)
- `go mod verify`
- `make release-v1` and `make release` (CGO-disabled Linux amd64 builds)
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` (no vulnerabilities found)

Live authenticated custom-TUN packet round trips and privileged host networking
still require disposable test tailnets and an appropriate Linux test host.

## Sources

- [Tailscale changelog](https://tailscale.com/changelog)
- [Tailscale v1.104.7 go.mod](https://github.com/tailscale/tailscale/blob/v1.104.7/go.mod)
- [Tailscale v1.104.7 tsnet implementation](https://github.com/tailscale/tailscale/blob/v1.104.7/tsnet/tsnet.go)
- [WireGuard TUN interface at the selected revision](https://github.com/tailscale/wireguard-go/blob/9e0ab45c16ca/tun/tun.go)
- [SQLite documentation and libc compatibility warning](https://pkg.go.dev/modernc.org/sqlite)
- [SQLite v1.62.0 module requirements](https://proxy.golang.org/modernc.org/sqlite/@v/v1.62.0.mod)
- [Maintained YAML fork](https://github.com/yaml/go-yaml)
