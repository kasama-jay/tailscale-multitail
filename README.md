# tailscale-multitail

Linux daemon that connects one host to multiple Tailscale tailnets using one
upstream `tsnet.Server` per profile, one host TUN, merged MagicDNS, synthetic
effective IPv4 addresses, and ordered raw-canonical IPv4 routing.

> v1 replaces normal host-wide `tailscaled`. Stop and disable `tailscaled`
> before starting this daemon.

## Build and test

```sh
go test ./...
make release-v1 V1_VERSION=1.0.0
```

Artifacts are written under `dist/tailscale-multitail_1.0.0_linux_amd64/`.
The pinned upstream version is recorded in `go.mod`.

## Install

Create the management group, install the binaries/unit, and initialize config:

```sh
sudo groupadd --system tsmultitail
sudo usermod -aG tsmultitail "$USER"
sudo install -m755 tailscale-multitaild tsmultitail /usr/bin/
sudo install -m644 tailscale-multitail.service /etc/systemd/system/
sudo tsmultitail config init
sudo tsmultitail profiles add work --hostname myhost-work
sudo tsmultitail profiles add home --hostname myhost-home
sudo systemctl daemon-reload
sudo systemctl enable --now tailscale-multitail
```

Authenticate a running profile without placing a key in argv or a file:

```sh
read -rsp 'Auth key: ' KEY; echo
printf '%s' "$KEY" | sudo tsmultitail profiles login work --auth-key-stdin
unset KEY
```

Interactive login is also supported; omit `--auth-key-stdin` and open the
returned URL. Profile order controls raw canonical-IP first-match selection.
Config changes are atomically written through the authenticated control socket
while the daemon runs and take effect after:

```sh
tsmultitail daemon restart
```

Useful checks:

```sh
tsmultitail validate
tsmultitail doctor
tsmultitail status
```

## Runtime behavior

- strict versioned YAML config at `/etc/tailscale-multitail/config.yaml`;
- profile state and SQLite effective leases in
  `/var/lib/tailscale-multitail/`;
- root-owned, group-authorized control socket using `SO_PEERCRED`;
- `multitail0`, routing table 552, and reserved rule priorities 5260–5269;
- effective A/PTR records plus profile-scoped DNS forwarding through
  systemd-resolved without claiming the default DNS route;
- IPv4 TCP, UDP, ICMP, fragmented traffic, bounded flow/fragment state, and
  fixed inter-profile forwarding denial;
- live withdrawal/reconciliation when a profile stops running.

Auth keys are accepted only from authorized stdin forwarding or profile
environment variables and are never written to YAML, SQLite, or status output.
`--debug-packets` is intended only for temporary diagnostics because it exposes
network metadata in logs.

See `PLAN.md`, `docs/architecture.md`, `docs/command-line.md`,
`docs/operations.md`, and `docs/security-review.md` for detailed semantics,
operations, and security boundaries.

## Milestone 0.5 feasibility harness

The historical custom-TUN/LocalAPI gate remains available as
`tailscale-multitail-feasibility`. It is opt-in and requires disposable test
keys; see `docs/milestone_0.5_results.md` and the `milestone-0.5` branch.
