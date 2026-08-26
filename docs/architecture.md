# Architecture

## Overview

This project is a Linux-only multi-tailnet host networking daemon built on top of upstream Tailscale `tsnet`.

Primary daemon binary: **`tailscale-multitaild`**

The host joins multiple tailnets simultaneously by running one embedded Tailscale node per profile, all inside one daemon process. Linux applications see one host-visible TUN interface and one local DNS service.

The daemon is responsible for:
- profile lifecycle
- aggregate peer/service inventory
- host routes
- DNS policy
- packet selection and translation
- effective-IP allocation
- ambiguity handling for overlapping canonical Tailscale IPs

## Design goals

- Use upstream `tsnet` without forking.
- Support whole-OS networking, not just per-app proxying.
- Preserve Tailscale's native control plane, auth, ACLs, DERP, WireGuard, and peer discovery inside each profile.
- Allow collision-free access through local effective IPs.
- Also permit raw canonical Tailscale IP routing for compatibility with external systems and A records.
- Use a human-readable config file plus CLI commands to inspect and manage it.

## Core model

### Profile

A **profile** represents one embedded Tailscale node and one tailnet membership.

Each profile has its own:
- stable profile ID and explicitly configured hostname; it may be reused across profiles because Tailscale scopes hostname uniqueness to a tailnet
- `tsnet.Server`
- daemon-derived state directory: `/var/lib/tailscale-multitail/<profile-id>`
- machine identity
- node identity
- preferences
- peer netmap
- LocalAPI client
- internal packet TUN

The physical Linux host therefore appears as a separate node in each joined tailnet.

### Host interface

The daemon creates one Linux TUN interface, named `multitail0` by default, with a global MTU default of `1280`. The MTU is configurable globally in v1; per-profile MTU adaptation is out of scope.

The Linux OS routes traffic for:
- effective IPs
- raw canonical peer/Service IPs

through this shared host TUN.

Default interface name: `multitail0`

### Internal profile TUNs

Each profile gets a channel-backed in-process TUN passed to upstream `tsnet.Server.Tun`.

The daemon sits between:
- the host TUN
- all profile TUNs

and performs packet steering plus any required address translation.

## High-level packet flow

```text
Linux application
  -> host routing
  -> shared host TUN
  -> daemon mux
  -> selected profile TUN
  -> selected tsnet engine
  -> tailnet peer/service
```

Inbound replies follow the reverse path.

## Subsystems

## 1. Supervisor

The supervisor owns process lifecycle and orchestration.

Responsibilities:
- load persistent state/config
- create profiles in configured order
- start/stop/restart profile engines
- degrade per profile: on failure/logout, withdraw that profile's routes, DNS records, and flows while other profiles continue; retry transient engine failures with backoff, but never automatically re-login an explicitly logged-out profile
- subscribe to LocalAPI updates
- rebuild aggregate routing/DNS/peer plans
- apply host TUN and DNS configuration atomically as far as practical

## 1a. Configuration model

The project will use a human-readable **YAML** config file.

V1 is a system daemon. Its authoritative config is `/etc/tailscale-multitail/config.yaml`; persistent profile state is under `/var/lib/tailscale-multitail/`.

`tsmultitail` uses a daemon-owned Unix-domain control socket for privileged operations and live status. The socket is local-only, root-owned, group-owned by `tsmultitail`, mode `0660`, and authorizes callers using Unix peer credentials. Members of that group may manage profiles and read live metadata.

The config has a required schema version (`version: 1` in v1) and uses strict decoding: unknown fields are rejected rather than silently ignored. It should include at least:
- effective IP CIDR/pool
- ordered profile list
- profile names/immutable IDs and explicitly configured hostnames; hostname reuse across distinct tailnets is allowed
- daemon-derived per-profile state directories under `/var/lib/tailscale-multitail/`
- enrollment configuration for interactive login and/or auth-key bootstrap
- optional per-profile HTTPS control URL (default: upstream Tailscale control plane); only HTTPS with normal TLS certificate validation is accepted, with no insecure override
- profile names constrained to `[A-Za-z][A-Za-z0-9_]*` and unique after uppercase normalization, preventing auth-key environment-variable collisions
- optional validated per-profile advertised `tag:` values, subject to the tailnet's tag-owner policy
- persistent profile identity only; ephemeral tsnet profiles are out of scope for v1
- any future daemon options

There should also be CLI commands to inspect and manage this config.

Config management is CLI-first:
- while the daemon runs, authorized CLI config commands use its Unix control socket and the daemon performs atomic, locked YAML rewrites
- initial config creation before the daemon exists requires root/sudo
- manual editing is still supported for administrators
- in v1, most changes take effect only after daemon restart; authorized `tsmultitail daemon restart` requests a clean daemon exit and the systemd unit starts the new generation. As an exception, login reads the authoritative config and starts newly added profiles so `profiles add` can be followed immediately by login; changes to existing profiles, removals, ordering, and global settings still require restart
- later we can add `SIGHUP`-driven hot reload

Daemon-owned runtime state, including effective leases and allocator metadata, is stored transactionally in a root-owned SQLite database under `/var/lib/tailscale-multitail/`. It is separate from user-managed YAML and must never contain plaintext auth keys. A missing or corrupt database is recreated automatically in v1, with a prominent warning that effective leases may have changed.

Command split:
- daemon: `tailscale-multitaild`
- management CLI: `tsmultitail`

The daemon exposes a project-specific, authenticated local control protocol over its Unix socket. This is not a compatibility implementation of Tailscale's native socket API.

## 2. Profile engine

Each profile engine wraps an upstream `tsnet.Server`.

Expected public interactions:
- `Start()` / `Close()`
- `LocalClient()`
- `Status()`
- `WatchIPNBus()`
- `QueryDNS()`
- `GetPrefs()` and other LocalAPI calls as needed

The daemon should avoid depending on unstable internals like `LocalBackend`.

## 3. Aggregate inventory

The daemon maintains an in-memory aggregate view across all profiles.

Suggested indexed views:
- profile -> status
- profile -> peers
- profile -> services
- FQDN -> record
- short hostname -> candidate records in profile order
- canonical IP -> ordered list of matching profiles/targets
- effective IP -> exact target mapping
- DNS suffix -> policy-selected profile

This aggregate view is the source of truth for routing and DNS decisions.

## 4. Effective IP allocator

Effective IPs are locally assigned synthetic addresses used to avoid collisions across tailnets.

Each lease is stable across restarts while the configured effective CIDR is unchanged and is keyed by a stable target identity, for example:

```text
(profile ID, stable target ID, address family) -> effective IP
```

V1 allocates with deterministic hashing plus collision probing from that stable identity, while persisting the result in SQLite. This improves address preservation after automatic state recreation for an unchanged inventory.

V1 defaults to `10.192.0.0/16`; an administrator may configure another IPv4 CIDR. The first usable address is reserved as the host NAT address, the second as the daemon DNS address, and leases begin at the third; none is leaseable to a remote target. Changing the configured CIDR between daemon runs flushes all old leases and allocates a new set. If active targets exceed allocatable addresses at startup or after a runtime inventory update, the daemon logs an actionable error and exits rather than partially operating. At startup, the daemon compares the pool with non-multitail host addresses and routes; any overlap is a fatal configuration error.

Targets include:
- direct peer nodes
- Tailscale Services if exposed as direct host-visible targets

### Why effective IPs are needed

Canonical Tailscale IPs can overlap across tailnets.

If the host tried to route directly to canonical peer IPs only, direct peer reachability would be ambiguous. Effective IPs solve this by ensuring one collision-free local dial target per target.

## 5. Raw canonical IP compatibility path

Effective IPs are not enough for all cases.

The system must also support routing to a raw canonical Tailscale IP, for example when:
- an external DNS server returns a canonical Tailscale IP in an A record
- a human or script uses the raw `100.x.y.z` address directly

### Canonical raw-IP routing rule

When a packet arrives for a canonical Tailscale destination IP that is **not** one of the locally assigned effective IPs:

1. inspect profiles in configured order
2. find the first active profile whose current peer or Service inventory contains that canonical IP
3. route through that profile
4. if later profiles also contain that IP, ignore them for this packet
5. if no active profile contains it, fail closed

Reply handling for these flows uses a bounded conntrack/state table so replies stay associated with the selected profile and preserve expected address semantics.

### Tradeoff

This is intentionally heuristic.

It does not eliminate ambiguity; it resolves ambiguity by ordered preference. In v1 this should be treated as normal expected behavior, not as a warning condition.

## 6. Packet mux and translator

The mux owns packet dispatch between the host TUN and the profile TUNs.

### Outbound decision order

For each outbound packet from the host TUN:

1. **Local service interception**
   - packets for the local DNS service IP should terminate inside the daemon
2. **Effective direct target lookup**
   - if destination is an effective IP, map directly to `(profile, canonical target)`
3. **Raw canonical Tailscale IP lookup by profile order**
4. otherwise drop/fail closed

### Host route model

Linux host route installation in v1 should be similar in spirit to Tailscale, but use a **different dedicated routing table**, not Tailscale's default table 52.

Default routing table: `552`

Requirements:
- numeric routing table ID is configurable; v1 does not modify `/etc/iproute2/rt_tables`
- daemon creates the necessary `ip rule` entries for lookup into that table
- v1 reserves `ip rule` priorities `5260`–`5269`, installs its primary lookup rule at `5260`, and exits if any priority in that range is occupied
- add one host route for each known canonical peer/Service IP via the shared host TUN
- add one host route for each effective IP via the shared host TUN

Example shape:
- `100.x.y.z dev multitail0`
- `10.192.37.102 dev multitail0`

This keeps routing narrow and target-specific rather than broadly routing the entire CGNAT/ULA space. V1 does not detect or specially handle conflicts between a canonical target and an existing non-multitail host route/address; normal `ip rule` priority determines the outcome.

### Outbound address translation

For direct effective-IP targets:
- source: host NAT IP -> selected profile's canonical self IP
- destination: effective IP -> canonical target IP

For raw canonical Tailscale IP targets:
- source: host NAT IP -> selected profile's canonical self IP
- destination: preserved as canonical target IP

Subnet-route translation is out of scope for v1.

### Inbound address translation

The daemon supports tailnet-initiated connections to host-wide services in v1. An unsolicited inbound packet addressed to a selected profile's canonical self IP is accepted subject to that tailnet's ACL evaluation inside tsnet, translated to the host NAT destination, and injected into the host TUN.

For replies and inbound-initiated flows:
- destination: selected profile self IP -> host NAT IP
- source: a direct peer/service is translated to its unique effective IP, including for unsolicited inbound connections, so peers with colliding canonical IPs remain distinguishable to the host
- a raw canonical-IP outbound flow preserves canonical source semantics on its replies, using conntrack/state to associate the reply with its originally selected profile
- host replies to an inbound-initiated flow are matched to the selected profile and translated back to that profile self IP and the remote canonical target

### Protocol and fragmentation scope

V1 supports IPv4 TCP, UDP, ICMP, and fragmented IPv4 traffic. The mux must perform bounded fragment association/reassembly as required for address translation, validate lengths, and recompute IPv4 and transport checksums. Resource limits, expiry, and drop counters are mandatory because fragmented packets are attacker-controlled input. Default idle expiry is TCP 5 minutes, UDP 60 seconds, and ICMP/fragment state 30 seconds. V1 caps state at 65,536 concurrent flow entries and 8,192 fragment-reassembly entries. On resource pressure it must first expire only idle protocol/fragment state, never evict active flows, then drop new flow/fragment traffic with counters and rate-limited logs. IPv6 is deliberately deferred, while data models and interfaces remain family-aware.

### Conntrack/state

A bounded conntrack/state table is part of the v1 design.

Primary purposes:
- keep raw canonical-IP reply handling consistent
- associate inbound and outbound flows with their selected profile
- support tailnet-initiated host connections
- preserve expected address semantics for host applications
- enforce bounded state, protocol-specific expiry, and fail-closed behavior on a miss

## 7. Host NAT address model

The host TUN should own reserved local addresses per family used for source translation and local services.

Example concept:
- the first usable IPv4 address in the effective pool as host NAT (default `10.192.0.1`)
- the second usable IPv4 address as the daemon DNS service (default `10.192.0.2`); systemd-resolved is configured to use it for `multitail0`
- one IPv6 host NAT address from the effective IPv6 pool when IPv6 is implemented

These addresses are assigned to `multitail0` as IPv4 `/32`s, not with the effective-pool prefix, so Linux creates no broad connected route. They are host-local and never advertised into any tailnet.

## 8. DNS system

The daemon runs a local DNS service and integrates it with Linux. V1 requires systemd-resolved.

### DNS transport

The local listener serves DNS over UDP and TCP port 53 and supports EDNS with an advertised 1232-byte UDP payload. It does not perform DNSSEC validation; validation policy remains with systemd-resolved and/or the selected upstream resolver.

### DNS responsibilities

- answer merged MagicDNS records
- answer effective-IP records for peers/services
- support ordered short-name search across profiles
- forward tailnet-zone DNS queries through the correct profile when local inventory cannot answer, rewriting any mapped canonical direct-peer/Service A record to its effective IP while preserving unrelated public records and CNAME structure; rewritten records use TTL `min(upstream TTL, 30 seconds)`
- preserve Linux-wide resolver usability without becoming the default DNS route

### Resolution rules

#### Fully-qualified names

If a query matches a known tailnet MagicDNS suffix:
- choose the corresponding authoritative profile
- answer from local inventory when possible
- otherwise use profile-scoped DNS querying

Duplicate MagicDNS suffixes or split-DNS routing suffixes across active profiles are fatal: the daemon must not start or continue with an ambiguous DNS routing plan.

#### Bare names

For a short name like `db`:
- search profiles in configured order
- return the first match
- if multiple profiles would match, later matches are ignored

This behavior is intentionally convenient rather than deterministic across profile reorderings.

#### DNS answers for direct peers/services

MagicDNS behavior in v1 is:
- return an **effective IPv4 A record** for direct peer/service names
- return NODATA for AAAA queries until effective IPv6 datapath exists
- never return canonical Tailscale IPs for these direct MagicDNS answers
- answer PTR queries in the effective IPv4 CIDR authoritatively with the leased target's MagicDNS FQDN
- use a 30-second TTL for synthesized effective A and PTR records; the daemon adds no second cache for these records

Reason:
- avoids collisions
- gives the host a stable unambiguous routing target
- does not advertise an IPv6 route that v1 cannot carry

#### DNS routing scope

V1 configures systemd-resolved with the daemon's dedicated DNS address and only known MagicDNS suffixes plus Tailscale reverse-DNS domains and the configured effective-CIDR reverse zone on `multitail0`. Unique tailnet suffixes are installed as host DNS search domains in configured profile order so bare names expand in that order; they are not merely route-only `~suffix` domains. The daemon must not claim the default `~.` routing domain. Existing host DNS remains responsible for public DNS and unrelated split-DNS domains.

#### DNS answers from external resolvers

If a non-local resolver returns a canonical Tailscale IP:
- the packet routing layer still needs to work
- that is why raw canonical IP routing exists

## 9. Linux integration

The project is Linux-only and manages only the initial host network namespace. Containers work only when their networking already uses that namespace; v1 does not create or manage named/container network namespaces.

Expected Linux integration areas:
- TUN creation/configuration
- route install/remove
- no firewall or sysctl mutation in v1; `doctor` detects likely blocking firewall/`rp_filter` policy and reports remediation guidance
- systemd-resolved per-link DNS server and routing-domain configuration/reconciliation for `multitail0`
- a hardened v1 systemd unit, including runtime-directory/socket ownership, network/systemd-resolved ordering, and least-required-capability review. A documented daemon exit status requests a controlled CLI restart; permanent config/host-conflict failures do not restart-loop, while transient runtime failures retry with rate limits.

### Native Tailscale exclusion

V1 requires normal host-wide `tailscaled`/`tailscale0` to be inactive. At startup, the daemon detects either condition and exits with an actionable error rather than attempting coexistence, because routes and systemd-resolved configuration would conflict.

## 10. Security boundaries

This project does **not** merge tailnet identities.

Each profile remains a separate Tailscale node from the point of view of the control plane.

ACL evaluation still happens inside the selected tailnet/profile using canonical Tailscale identity.

### Inter-tailnet forwarding policy

V1 blocks mux-mediated L3 forwarding between profiles. The mux accepts profile-originated traffic only when it is addressed to the host NAT endpoint or matches a host-originated flow, and it never injects a packet received from one profile into another profile. V1 is intended for locally originated host traffic and does not support or guarantee isolation from Linux forwarding traffic from other interfaces; it does not modify firewall rules or IP-forwarding sysctls. This is not a defense against application-layer bridging by the host or independent host routing/NAT rules outside the daemon.

### Important limitation

The host becomes a point where multiple tailnets coexist.

This means:
- each tailnet can reach host-wide services where its tailnet ACL permits it
- a compromised host can intentionally or accidentally bridge information between tailnets
- the daemon should fail closed on routing uncertainty where possible
- we should document that this is a host-trust model, not a cryptographic tailnet-isolation model

## 11. Observability

The daemon should expose enough visibility to debug profile selection and collisions.

Logging policy: do not log individual packets or flows by default. Log lifecycle/config/route/DNS-plan changes, aggregate counters, and rate-limited errors. Flow-level profile/destination details require an explicit temporary debug mode because they expose tailnet metadata through journald.

Recommended diagnostics:
- active profile list and order
- per-profile health/state and retry/backoff state
- effective IP leases
- canonical IP overlap table
- raw-IP first-match selection behavior
- DNS suffix plan
- short-name collision list
- last-selected profile for a raw canonical IP lookup

## Key tradeoffs

## Effective IPs vs raw canonical IPs

Effective IPs are architecturally cleaner.

Raw canonical IP routing is less clean but necessary for compatibility with:
- user habits
- external DNS
- systems that already store canonical Tailscale IPs

The design therefore supports both, with effective IPs preferred for daemon-generated naming.

## Ordered first-match policy

Using profile order to break raw-IP ambiguity is simple and predictable enough for v1, but:
- profile order becomes semantically important
- reordering profiles can change behavior
- diagnostics must make this obvious

## Upstream-only `tsnet`

The project pins an exact tested upstream Tailscale module version. It does not promise compatibility with arbitrary newer releases; every upgrade requires a compatibility review and integration-test run, especially for custom-TUN and LocalClient behavior.

Benefits:
- lower maintenance burden
- easier upgrades
- fewer internals dependencies

Cost:
- we should build our own management/control surface rather than trying to fully emulate native per-profile `tailscale` CLI behavior

## Feasibility gate

Before the runtime implementation, pin an exact upstream Tailscale module version and prove with integration tests that a channel-backed custom `tun.Device` carries host-TUN-to-profile and reverse traffic with a real remote peer. The same gate must verify that supported public `LocalClient` APIs provide the required peer, Service, DNS, and netmap-change inventory, profile-scoped DNS querying, and the intended inbound packet path. Do not depend on `LocalBackend` or other unstable internals.

## Initial recommended scope

V1 should focus on:
- multiple profiles via upstream `tsnet`
- aggregate peer inventory
- effective-IP allocator
- host TUN datapath
- merged DNS with ordered short-name search
- raw canonical IP first-match routing for both peers and Services IPs
- YAML config plus CLI-first management
- restart-required config application
- conntrack/state for raw canonical-IP reply handling
- target-specific host routes in a configurable dedicated routing table
- daemon-managed `ip rule` integration for that table

V1 intentionally excludes:
- subnet-route support: configure profiles not to accept or advertise subnet routes
- exit-node support: configure profiles not to select or advertise an exit node
- Tailscale Serve, Funnel, and Tailscale SSH; profiles must not expose these independently of the host-TUN mux
- native Tailscale socket/API compatibility (the project-specific authenticated Unix control protocol is required in v1)
- hot reload

V1 can defer or simplify:
- advanced policy management API
- packaging beyond the v1 systemd unit

## IPv6 stance

The design remains IPv6-aware so future extension is clean, but IPv6 datapath support is deferred from v1.

## Routing-table naming

V1 uses the configured numeric routing-table ID only (default `552`) and does not modify `/etc/iproute2/rt_tables`. Diagnostics may display a daemon-local label.
