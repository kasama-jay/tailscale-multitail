# v1 security review

## Trust and privilege boundary

`tailscale-multitaild` runs as root because it creates a TUN device, installs
routes/rules, binds DNS port 53, and configures systemd-resolved. Its management
socket is root-owned, group-owned by `tsmultitail`, mode `0660`, and checks
kernel-provided Unix peer credentials including supplementary group membership.
The server bounds concurrent connections, applies a short I/O deadline, limits
request fields, and rejects unexpected secret-bearing fields.

Membership in `tsmultitail` delegates a deliberately narrow operational role:
live status, login, logout, and controlled daemon restart. It does **not**
delegate arbitrary configuration writes. `config_write` is accepted only from
UID 0, so a group member cannot choose root-owned config paths, interface/table
settings, a custom control server, profile tags, or future root-executed
configuration fields. Profile additions/config edits therefore require root
(or a declarative system configuration such as NixOS).

A group member can still deliberately disrupt the delegated service by logout
or restart; that is inherent in granting those operations and should be treated
as network-operator authority, not general root authority.

The systemd unit restricts capabilities to `CAP_NET_ADMIN` and
`CAP_NET_BIND_SERVICE`, enables `NoNewPrivileges`, and applies filesystem and
kernel protection directives. Operators should keep `/etc/tailscale-multitail`,
`/var/lib/tailscale-multitail`, and group membership root-administered.

## Secrets and identity

Auth keys are accepted only from an authorized stdin-forwarded control request
or a profile-specific daemon environment variable. They are not written to
YAML, SQLite, status output, or normal logs. Avoid shell history and remove
transient environment/drop-in files after bootstrap. Persistent identities need
non-ephemeral enrollment; ephemeral keys intentionally do not survive restart.

## Network isolation and routing

The mux never injects a packet received from one profile into another profile;
inter-tailnet L3 forwarding is fixed-deny. This is not a host firewall:
applications can still bridge traffic, and external forwarding/sysctl/firewall
configuration remains the administrator's responsibility.

Effective addresses prevent canonical-IP collisions for direct access. Raw
canonical destinations deliberately use configured first-match profile order,
so collisions are ambiguous by design. Prefer effective addresses where that
ambiguity matters.

The daemon rejects native `tailscaled`/`tailscale0`, reserved rule-priority
conflicts, and effective-pool overlap with host addresses/routes. It does not
manage arbitrary pre-existing canonical-route conflicts; normal Linux policy
routing precedence applies.

## DNS

The local DNS mux handles only configured tailnet suffixes and does not claim
`~.`. `multitail0` is configured with per-link `DNSSEC=no`: synthesized and
forwarded responses are unsigned, so inheriting a global `DNSSEC=yes` policy
would make MagicDNS unusable. Public DNS remains on existing host links.

## Resource exhaustion and observability

Flow state is capped at 65,536 entries and fragment state at 8,192 entries,
with protocol-aware expiry. Withdrawn profiles purge their associated state.
The status API exposes drops, capacity drops, current state, purge totals, and
rate-limited operational errors. `--debug-packets` can expose network metadata
and should be temporary only.

## Residual risks

This is privileged beta software built around upstream `tsnet` behavior.
Review Tailscale ACLs/Service grants per profile, keep an out-of-band recovery
path, and test upgrades/rollback on a non-critical machine before relying on
it for important connectivity.
