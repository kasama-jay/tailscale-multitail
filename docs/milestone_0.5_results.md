# Milestone 0.5 feasibility results

## Result

The upstream-only `tsnet` design passed the feasibility gate. A process can
run multiple independent embedded Tailscale profiles with separate state
roots, use a channel-backed custom `tun.Device` for each profile, and obtain
the required routing/DNS inventory through supported public LocalAPI methods.

The implementation and executable test harness are on the `milestone-0.5`
branch. This document records the results without including auth keys, tailnet
names, or other credentials.

## Test topology

The harness creates:

- profile **A** and profile **B**, each using a reusable ephemeral auth key in
  one test tailnet;
- profile **C**, using a reusable ephemeral auth key in a distinct test
  tailnet;
- a normal, separately hosted Tailscale node in C's tailnet that accepts ICMP;
- a pre-existing Tailscale Service in C's tailnet.

Every profile uses a distinct temporary `tsnet.Server.Dir` and a bounded
channel-backed `tun.Device`. No host Linux TUN, routes, DNS configuration, or
firewall changes are made by the feasibility harness.

## Validations performed

### Custom-TUN packet path

The harness injects a complete IPv4 ICMP echo packet into A's custom TUN,
addressed to B's canonical Tailscale IPv4 address. The packet is received from
B's custom TUN. It repeats the operation in reverse. This proves that upstream
`tsnet.Server.Tun` accepts a custom device and that both transmission and
receipt traverse the device boundary.

The custom device copies packet buffers at both boundaries and uses bounded
queues, so test callers and tsnet may safely reuse their buffers.

### External inbound path

Profile C injects an ICMP echo request to the normal external Tailscale node
in C's tailnet. The node's echo reply is emitted by C's custom TUN and is
validated to have the expected source and destination addresses. This removes
the same-process/same-host ambiguity of the A/B test and demonstrates the
intended remote-peer-to-profile packet direction.

### Peer inventory and netmap updates

The harness starts A, subscribes to `LocalClient.WatchIPNBus` with
`NotifyInitialState | NotifyPeerChanges`, then starts B. It requires a
`PeersChanged` notification containing B's exact node ID.

It also calls `LocalClient.Status` on both profiles and verifies that the
other profile is represented with:

- a stable node ID;
- a canonical Tailscale IPv4 address; and
- a MagicDNS FQDN.

This confirms the public API supplies the peer identity and addressing needed
for the aggregate inventory and detects incremental peer additions. Runtime
implementation should still handle removals and patches using the same watch
stream.

### DNS scope across tailnets

For every profile, the harness uses `LocalClient.QueryDNS` to query its own
MagicDNS FQDN. It verifies that profile C has a different
`CurrentTailnet.MagicDNSSuffix` from A. This demonstrates that DNS queries and
DNS inventory are profile-scoped rather than process-global.

### Tailscale Service inventory

On profile C, the harness calls `LocalClient.GetServices` and verifies that
the known Service is returned with its expected IPv4 Service address and
advertised TCP port. It also issues a profile-scoped DNS query for the
Service FQDN.

The harness intentionally does **not** use `tsnet.HTTPClient` to test an HTTP
response while a custom TUN is installed. That client needs a local host-side
network stack/mux to route reply packets back to its connection; this project
has not implemented that mux yet. An HTTP-client timeout in this configuration
is therefore not evidence of a Service or tsnet failure. The Service inventory
and packet-level datapath assumptions needed for the next milestone are
validated separately.

## Public APIs relied upon

The gate uses only supported exported interfaces:

- `tsnet.Server` (`Tun`, `Up`, `TailscaleIPs`, `LocalClient`)
- `local.Client.Status`
- `local.Client.WatchIPNBus`
- `local.Client.QueryDNS`
- `local.Client.GetServices`

It does not access `LocalBackend` or other tsnet/tailscaled internals.

## Remaining work

Passing this gate does not implement the daemon. The following remain for
subsequent milestones:

- profile supervisor and durable configuration/state handling;
- aggregate inventory reconciliation and collision indexes;
- Linux host TUN, routing-table, and `ip rule` management;
- host-side packet mux, effective-IP translation, conntrack, and fragmentation
  handling;
- DNS server and systemd-resolved integration; and
- privilege, control-socket, deployment, and recovery hardening.
