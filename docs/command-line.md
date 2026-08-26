# Command line

## Overview

This document proposes the command-line interface for `tailscale-multitail`.

Primary daemon binary:
- `tailscale-multitaild`

CLI binary:
- `tsmultitail`

V1 is a system-daemon design. Management is CLI-first, with a project-specific authenticated Unix control socket:
- the authoritative config is `/etc/tailscale-multitail/config.yaml`
- YAML has a required schema version and strict decoding rejects unknown fields
- while the daemon runs, authorized config commands use its local socket and the daemon atomically rewrites canonical YAML
- `config init` before the daemon exists requires sudo/root
- the CLI uses the local daemon socket for login, logout, and live status
- config changes take effect after daemon restart

## Config file and control socket

Default config path: `/etc/tailscale-multitail/config.yaml`.

Default daemon control socket path: `/run/tailscale-multitail/control.sock`. It is local-only, root-owned, group-owned by `tsmultitail`, mode `0660`, and authorizes callers based on Unix peer credentials. Group members may manage profiles and read live metadata. Global config-selection flags may override the config path for explicit administrative or test use.

## CLI shape

split binaries for daemon and CLI:
   - `tailscale-multitaild`
   - `tsmultitail`

The CLI manages config and invokes daemon-side profile login/logout and live-status operations over the authenticated local control socket.

## Global flags

Available on most subcommands:

- `--config <path>`
  - explicit config file path
- `--verbose`
  - increase log verbosity
- `--json`
  - JSON output for machine-readable commands where applicable

Daemon-only flags may also include:

- `--foreground`
  - stay in foreground instead of daemon-style service behavior
- `--validate-config`
  - validate config and exit

## Top-level commands

## `tailscale-multitaild run`

Start the daemon.

Example:

```bash
tailscale-multitaild run
```

Flags:
- `--config <path>`
- `--foreground`
- `--verbose`
- `--validate-config`

Behavior:
- loads config
- starts all configured profiles in order
- creates `multitail0` unless overridden by config
- applies routes and `ip rule` entries using routing table `552` unless overridden by config; v1 reserves priorities `5260`–`5269` and installs the primary lookup rule at `5260`, failing on any conflict
- runs until terminated

## `tsmultitail daemon restart`

Request a clean restart of the running daemon through the authenticated control socket. The systemd unit restarts it, applying the current config generation.

Example:

```bash
tsmultitail daemon restart
```

Flags:
- `--config <path>`

## `tsmultitail validate`

Validate the config file and print any errors.

Example:

```bash
tsmultitail validate
```

Flags:
- `--config <path>`
- `--json`

## `tsmultitail config show`

Print the current config.

Example:

```bash
tsmultitail config show
```

Flags:
- `--config <path>`
- `--json`

## `tsmultitail config init`

Create a new config file with defaults if one does not exist.

Example:

```bash
tsmultitail config init
```

Behavior:
- before the daemon exists, this command requires sudo/root to create the root-owned system config
- while the daemon runs, it uses the authorized control socket

Flags:
- `--config <path>`
- `--force`

Default initialized values should include:
- interface name: `multitail0`
- routing table: `552`
- MTU: `1280`
- effective IPv4 CIDR: `10.192.0.0/16`
- empty profile list

## `tsmultitail config set`

Set top-level config values.

Examples:

```bash
tsmultitail config set interface multitail0
tsmultitail config set routing-table 552
tsmultitail config set mtu 1280
tsmultitail config set effective-ipv4-cidr 10.192.0.0/16
```

Supported keys in v1:
- `interface`
- `routing-table`
- `mtu`
- `effective-ipv4-cidr`

Flags:
- `--config <path>`

## `tsmultitail profiles list`

List configured profiles in order.

Example:

```bash
tsmultitail profiles list
```

Flags:
- `--config <path>`
- `--json`

## `tsmultitail profiles add`

Add a profile to the config.

Example:

```bash
tsmultitail profiles add work --hostname host-work --control-url https://control.example.net
```

Arguments:
- `<name>` profile name

Flags:
- `--hostname <hostname>` required; it may be reused across profiles because uniqueness is scoped to each tailnet
- `--profile-id <UUID>` optional explicit restore of retained state; otherwise generated
- `--control-url <https-url>` optional per-profile control URL; defaults to upstream Tailscale control plane and must use HTTPS with normal TLS certificate validation (no insecure override)
- `--advertise-tag <tag:...>` repeatable optional tag; tag ownership remains enforced by the selected tailnet
- normal invocation generates a profile UUID; `--profile-id <UUID>` explicitly restores a previously retained identity and reuses `/var/lib/tailscale-multitail/<UUID>` only after root ownership, mode, and exclusivity checks
- daemon derives the state directory as `/var/lib/tailscale-multitail/<profile-id>` and creates it root-owned with mode `0700`; normal CLI users cannot set it
- `--position <n>` optional insert position
- `--after <profile>` optional
- `--before <profile>` optional
- `--config <path>`

Notes:
- order matters for raw canonical-IP first-match routing
- profile names must be unique case-insensitively and match `[A-Za-z][A-Za-z0-9_]*`, so `TAILSCALE_AUTH_KEY_<UPPERCASE_PROFILE_NAME>` is unambiguous

## `tsmultitail profiles remove`

Remove a profile from the config.

Example:

```bash
tsmultitail profiles remove work
```

Behavior:
- removes only the profile configuration by default and retains its durable state directory; the running daemon retains the old runtime profile until its required restart
- state deletion requires `--purge-state` and `--yes`; it is irreversible
- purge is rejected while the profile is active or remains in the running daemon generation. Remove it, restart the daemon, then purge its retained state.

Flags:
- `--config <path>`
- `--purge-state`
- `--yes` required with `--purge-state`

## `tsmultitail profiles move`

Reorder a profile.

Examples:

```bash
tsmultitail profiles move work --before home
tsmultitail profiles move work --position 0
```

Flags:
- `--before <profile>`
- `--after <profile>`
- `--position <n>`
- `--config <path>`

## `tsmultitail profiles set`

Modify profile settings.

V1 permits `profiles set <name> --control-url <https-url>` to change a profile control URL in config; the change takes effect on daemon restart. Other profile settings are immutable after creation.

## `tsmultitail profiles login`

Start a login flow for a configured profile.

Example:

```bash
tsmultitail profiles login work
```

Arguments:
- `<name>` profile name

Flags:
- `--config <path>`

V1 behavior:
- it reads the authoritative YAML and starts a profile newly added since daemon startup, so `profiles add` may be followed immediately by login. Changes to existing profile configuration still require daemon restart.
- it is implemented by the running daemon through its authenticated Unix control socket; it must not invoke a separate `tailscale` process against a live tsnet state directory
- supports an interactive browser/device login flow and reports the URL/instructions to the caller
- supports auth-key bootstrap for unattended enrollment
- plaintext auth keys must not be persisted after successful enrollment
- auth keys may be supplied through `--auth-key-stdin` over the authorized Unix socket or daemon environment variable `TAILSCALE_AUTH_KEY_<UPPERCASE_PROFILE_NAME>`; file-path input and literal command-line key values are not accepted

Expected output:
- login URL or login instructions
- clear indication of which profile is being authenticated

## `tsmultitail profiles logout`

Log out a configured profile.

Example:

```bash
tsmultitail profiles logout work
```

Arguments:
- `<name>` profile name

Flags:
- `--config <path>`
- `--yes` required

V1 behavior:
- this command is executed by the running daemon through its authenticated Unix control socket
- it affects only the selected profile, immediately withdraws its routes/DNS and purges profile-owned mux flow/fragment state before transitioning it to `NeedsLogin`
- it retains the profile configuration and state directory for later re-login; state deletion remains the explicit `profiles remove --purge-state --yes` operation

## `tsmultitail status`

Print live daemon status as JSON through the authenticated local control socket.

The response contains profile state, ordered peer/Service targets, effective leases, and `datapath` counters. Node targets include `online`, which reflects upstream Tailscale control-plane presence. Service targets do not have peer online state.

`datapath` is the v1 metrics surface: `host_packets`, `profile_packets`, `drops`, flow/fragment-capacity drops, current flow/fragment counts, profile-state purge totals, and emitted rate-limited operational errors. Counters reset when the daemon restarts.

Global flags must precede the command:

```sh
tsmultitail --socket /run/tailscale-multitail/control.sock status
```

## `tsmultitail doctor`

Run local diagnostics without changing state.

Suggested checks:
- config parses
- interface name is valid
- routing table value is valid
- effective IPv4 CIDR is valid and does not overlap any non-multitail host address or route
- daemon-derived profile state paths are accessible and safely owned
- config has no duplicate profile names after uppercase normalization
- likely blocking firewall and reverse-path-filter (`rp_filter`) policy; v1 reports remediation but does not mutate firewall/sysctl settings

Flags:
- `--config <path>`
- `--json`

## `tsmultitail example-config`

Print an example YAML config to stdout.

Example:

```bash
tsmultitail example-config
```

## Proposed flags and semantics

## Interface/routing settings

These should live in config, not primarily on the daemon command line:

- `interface`
  - default: `multitail0`
- `routing_table`
  - numeric ID only; default: `552`; v1 does not modify `/etc/iproute2/rt_tables`
- `mtu`
  - global host-TUN MTU; default: `1280`; no per-profile MTU adaptation in v1
- `effective_ipv4_cidr`
  - default: `10.192.0.0/16`; changing it between daemon runs flushes effective leases and assigns new ones

## Proposed YAML shape

Illustrative only:

```yaml
version: 1
interface: multitail0
routing_table: 552
mtu: 1280
effective_ipv4_cidr: 10.192.0.0/16
profiles:
  - id: 2d2e18db-2d3c-4bee-8e57-a456b2451e5d
    name: work
    hostname: host-work
    control_url: https://control.example.net
    advertise_tags:
      - tag:server
  - id: 710c05fb-5c34-4ed8-8f16-ee993ce7c647
    name: home
    hostname: host-home
```

## UX principles

- Reordering profiles must be easy, because order affects raw canonical-IP routing.
- Config-mutating commands should rewrite YAML deterministically.
- Commands should be idempotent where practical.
- Validation errors should be explicit and actionable.
- Nothing should silently require a hot reload in v1; restart is the mechanism.

## Future commands (not v1)

Potential future additions:
- `tsmultitail reload`
- `tsmultitail status --live`
- `tsmultitail routes show`
- `tsmultitail dns show`
- `tsmultitail profiles up/down <name>`
