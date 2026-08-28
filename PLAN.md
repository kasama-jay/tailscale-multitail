# PLAN

## Project

**tailscale-multitail**

Primary daemon binary: **`tailscale-multitaild`**

Linux-only daemon for connecting one host to multiple Tailscale tailnets at the same time using upstream `tsnet`, one embedded profile per tailnet, one host-visible TUN, one local DNS service, and a packet mux/translation layer.

## Current implementation status

`master` is an actively tested Linux beta, not yet a final v1 declaration. The latest published artifact is `v1.0.0-beta.6`; `master` also contains subsequent CLI/style work awaiting the next bundled beta.

Implemented and exercised on the playground VM:

- upstream `tsnet` profiles with isolated state and internal TUNs;
- strict YAML configuration, atomic daemon-owned rewrites, and `flag`-based CLI parsing;
- authenticated Unix control socket for status, restart, profile login/logout, and live profile addition;
- persistent effective IPv4 leases, host-TUN routing, ordered raw-IP selection, IPv4 TCP/UDP/ICMP translation, and bounded fragment/flow state;
- host-TUN batch draining (required for bursty TCP/SSH traffic), with non-blocking per-profile injection;
- merged effective-IP DNS over UDP/TCP, systemd-resolved per-link configuration, reverse zones, and per-link DNSSEC/DNS-over-TLS disabled because the local mux is unsigned plaintext DNS;
- profile degradation/reconciliation, systemd lifecycle, diagnostics, state corruption recovery, and control-socket authorization.

Validated VM scenarios include effective/raw peer connectivity, HTTP to an ordinary peer, fragmented ICMP, DNS/PTR resolution, login/logout, live add-then-login, control-group access, restart/cleanup, and repeated OpenSSH connections using the default ML-KEM hybrid key exchange.

Still required before final-v1 sign-off:

- explicitly purge conntrack/fragment state owned by a degraded or logged-out profile;
- complete rate-limited operational-error reporting and a documented metrics surface;
- run the full multi-profile degradation/recovery and upgrade/rollback test matrix; and
- final documentation and security review.

## Goals

- Whole-OS connectivity across multiple tailnets at once.
- No fork of Tailscale code.
- Linux-only, initial host network namespace only; v1 does not manage named/container network namespaces.
- One embedded `tsnet.Server` per profile/tailnet.
- One shared host TUN visible to Linux applications.
- Local DNS service for merged MagicDNS behavior, integrated through systemd-resolved.
- Human-readable config file plus CLI management commands.
- Support both:
  - synthetic/effective IPs for collision-free access
  - raw canonical Tailscale IP routing with ordered profile selection

## Non-goals for v1

- macOS or Windows support
- native `tailscale --socket` compatibility for each profile
- avoiding all ambiguity in overlapping tailnets
- configurable inter-tailnet forwarding policy (v1 uses a fixed deny policy)
- subnet-route support: profiles must not accept or advertise subnet routes in v1
- exit-node support: profiles must not select or advertise an exit node in v1
- Tailscale Serve, Funnel, and Tailscale SSH; these are disabled/unsupported for all profiles
- zero-copy datapath optimization
- IPv6 datapath support

## Confirmed design constraints

- We will use **upstream `tsnet` only**, pinned to an exact tested Tailscale module version. Upgrades require compatibility review and integration testing.
- We will not fork `tsnet` or other Tailscale packages unless forced later by a hard blocker.
- The daemon replaces normal host-wide `tailscaled`: at startup v1 detects an active native `tailscaled` or `tailscale0` and exits with an actionable error.
- Effective IP NAT is acceptable.
- The primary daemon binary is `tailscale-multitaild`.
- The project will use a human-readable **YAML** config file.
- V1 is a system daemon: the authoritative config is `/etc/tailscale-multitail/config.yaml` and persistent profile state is under `/var/lib/tailscale-multitail/`.
- The management CLI uses a daemon-owned, root-controlled Unix socket for privileged and live operations; auth-key input is accepted only via authorized stdin forwarding or a daemon environment variable, never an arbitrary file path or literal CLI value.
- The config file will include at least:
  - a required schema version; v1 strictly rejects unknown fields to catch manual-edit errors
  - the effective IP CIDR/pool to use
  - the ordered list of Tailscale profiles
  - each profile's derived state directory (not user-selectable): `/var/lib/tailscale-multitail/<profile-id>`
  - a stable profile ID and explicitly configured hostname for each profile; hostname uniqueness is enforced by Tailscale within each tailnet, not across local profiles
  - profile enrollment configuration (interactive login and/or auth-key bootstrap); v1 has no destructive login-reset flag, so fresh identity creation uses the explicit remove/purge/re-add lifecycle
  - optional per-profile HTTPS control URL (default: upstream Tailscale control plane); require normal TLS certificate validation and permit no insecure override
  - profile names constrained to `[A-Za-z][A-Za-z0-9_]*` and unique after uppercase normalization, permitting collision-free auth-key environment variable names
  - optional validated per-profile advertised `tag:` values, subject to the tailnet's tag-owner policy
  - persistent profile identity only; ephemeral tsnet profiles are out of scope for v1
- There should be CLI commands to inspect and manage that config.
- Config management is CLI-first: CLI commands rewrite the config file, but manual editing is still supported.
- For **raw canonical Tailscale destination IPs**, routing will use **ordered profile selection**:
  1. check profiles in configured order
  2. select the first active profile whose peer or Service set contains the destination IP
  3. route through that profile

## Inter-tailnet forwarding policy

V1 blocks mux-mediated L3 forwarding between profiles. The mux only delivers profile-originated packets to the host NAT endpoint or to a matching host-originated conntrack flow; it never injects a packet received from one profile into another profile. V1 is intended for locally originated host traffic and does not support or guarantee isolation from Linux forwarding traffic from other interfaces; it does not modify firewall rules or IP-forwarding sysctls. This also does not prevent application-layer bridging by the host or separate host networking configuration outside the daemon.

## Important caveat

Ordered raw-IP routing is intentionally heuristic.

If two active profiles contain the same canonical Tailscale IP, then:
- the first matching profile wins
- behavior depends on profile order
- external DNS records pointing at canonical Tailscale IPs may resolve to an ambiguous destination
- this is expected and accepted behavior for v1

## Proposed architecture summary

- **daemon**
  - owns lifecycle, config, state, routing policy, DNS policy, observability
- **profile engine**
  - one `tsnet.Server` per profile, with a stable profile ID and configured hostname (which may be reused across distinct tailnets)
  - one internal channel-backed TUN per profile
  - one LocalAPI client per profile
- **host TUN**
  - one Linux TUN interface visible to the OS
- **packet mux**
  - receives packets from host TUN
  - chooses profile based on effective IP, explicit route, or raw canonical IP lookup
  - rewrites addresses as needed
  - injects packet into selected profile TUN
- **DNS service**
  - authoritative for merged MagicDNS/effective records
  - merged short-name search behavior across profiles
  - profile-scoped forwarding for tailnet DNS zones only; v1 does not become the host default DNS route
- **state store / config**
  - persistent profile definitions
  - profile ordering
  - effective IP leases
  - effective IP CIDR/pool selection
  - route and DNS policy
  - profile `Dir` locations
  - YAML config file as primary user-managed source of configuration
- root-owned SQLite runtime-state database under `/var/lib/tailscale-multitail/` for transactional effective leases and daemon metadata; never store plaintext auth keys. If missing or corrupt, v1 recreates it automatically and emits a prominent warning that effective leases may have changed.

## Routing policy model

### Direct node/service access

Preferred host-visible target form:
- effective IPv4 assigned by local allocator (IPv6 deferred)

Fallback host-visible target form:
- raw canonical Tailscale IPv4 (IPv6 deferred)

Raw canonical-IP support applies to both:
- peers
- Tailscale Services IPs

### Route selection order

For outbound packets:
1. explicit effective-IP direct target mapping
2. raw canonical Tailscale IP resolution by ordered profile scan
3. otherwise fail closed

Host route installation in v1 should be similar in spirit to Tailscale:
- install routes in a dedicated configurable routing table
- do **not** reuse Tailscale's default table 52
- the config should include the numeric routing table ID to use
- the daemon should create the necessary `ip rule` entries to direct matching traffic into this table
- v1 does not modify firewall rules or sysctls; `doctor` detects likely blocking firewall/`rp_filter` policy and reports remediation guidance
- v1 reserves `ip rule` priorities `5260`–`5269`, installs its primary lookup rule at `5260`, and exits if any priority in that range is occupied
- add one host route for each known canonical direct target IP via the shared host TUN
- v1 does not detect or specially handle conflicts between a canonical target and existing non-multitail host routes/addresses; normal `ip rule` priority determines the outcome
- add one host route for each effective IP via the shared host TUN
- example shape:
  - `100.x.y.z dev tailscalemultitail0`
  - `10.192.37.102 dev tailscalemultitail0`

### Raw canonical Tailscale IP behavior

When an IPv4 destination is in the Tailscale CGNAT range and is not an effective IP:
- scan profiles in configured order
- if the earliest matching active profile contains a peer or Service with that canonical IP, route there
- destination address remains canonical inside the selected profile
- source is rewritten from host NAT IP to that profile's canonical self IP

If multiple later profiles also contain that same canonical IP, they are ignored for that packet.

No warning is needed for this condition in v1; first-match resolution is the intended behavior.

Reply handling for raw canonical-IP flows should use a bounded conntrack/state table so replies remain associated with the selected profile and the expected address semantics. The same bidirectional flow model must support tailnet-initiated connections to host-wide services: unsolicited inbound packets to a profile self IP are permitted subject to that tailnet's ACLs, translated to the host NAT destination, and given a unique effective source IP for the remote peer/service.

## DNS policy model

### Name resolution behavior

- FQDN under a known tailnet suffix resolves using that profile
- bare names are searched across profiles in configured order
- first successful match wins
- duplicate MagicDNS or split-DNS routing suffixes across active profiles are a fatal configuration/runtime error
- MagicDNS answers for direct peer/service records return effective IPv4 A records, never canonical Tailscale IPs; until IPv6 datapath exists, AAAA queries return NODATA
- the local DNS service is authoritative for PTR records in the effective IPv4 CIDR and returns the leased target's MagicDNS FQDN
- synthesized effective MagicDNS A and PTR records have a 30-second TTL; the daemon adds no second cache for them

### External A-record case

If an external resolver returns a canonical Tailscale IP:
- the host routing layer should still be able to reach it using raw-IP ordered profile selection
- this is one of the reasons raw canonical IP support is required

## Workstream and milestone status

| Milestone | Status | Delivered scope / remaining work |
| --- | --- | --- |
| 0 — design | Complete | Requirements, architecture, routing/DNS policy, and security boundaries documented. |
| 0.5 — tsnet feasibility | Complete | Exact upstream module pinned; custom-TUN, LocalAPI inventory/DNS, and real-tailnet feasibility gate recorded in `docs/milestone_0.5_results.md`. |
| 1 — profile runtime | Complete | One `tsnet.Server` and internal TUN per profile, separate state directories, LocalAPI status/watchers, degradation/backoff. |
| 2 — aggregate model | Complete | Ordered peer/Service inventory, canonical collision preservation, deterministic effective leases, online state in live status. |
| 3 — DNS | Complete for beta | Effective A/PTR, ordered suffix forwarding, UDP/TCP+EDNS, DNS rewrite, resolved reconciliation, reverse route domains, DNSSEC/DNS-over-TLS-off link policy, and validated Service HTTPS DNS path. |
| 4 — host TUN | Complete for beta | Linux TUN, dedicated table/rules, dynamic `/32` routes, overlap/native-daemon protection, cleanup, and batched reads. |
| 5 — effective IPv4 datapath | Complete for beta | IPv4 TCP/UDP/ICMP, checksums, bounded fragments, effective inbound mapping, and VM HTTP/SSH plus known-good Service HTTPS validation. |
| 6 — raw canonical routing | Complete for beta | Ordered peer/Service inventory lookup, raw flow state, profile-self source translation, and VM ICMP/TCP validation. |
| 7 — deployment hardening | In progress | Systemd, resolved cleanup, control authorization, SQLite recovery, diagnostics, restart policy, profile-state purge, and status metrics are implemented. Full recovery matrix, release/runbook review, and final security review remain. |

The near-term priority is Milestone 7 closure, not new v1 features.
## Default values

- Default TUN interface name: `multitail0`
- Default host TUN MTU: `1280`, configurable globally; v1 has no per-profile MTU adaptation.
- Default routing table: `552`
- Default effective IPv4 CIDR: `10.192.0.0/16`; users may configure another IPv4 CIDR.
- Reserve the first usable IPv4 address in the effective CIDR for the host NAT endpoint and the second for local DNS; assign both to the host TUN as `/32`s, not with the pool prefix, so no broad connected route is created. Neither is leaseable to a remote target, and leases begin at the third usable address.
- Effective leases use deterministic hashing with collision probing from stable `(profile ID, target ID)` identity and are stable while the configured CIDR is unchanged. This improves address preservation after automatic state recreation. On CIDR change between daemon runs, flush all prior leases and allocate new ones. If active targets exceed allocatable addresses at startup or on a runtime inventory update, log an actionable error and exit rather than partially operating.
- At startup, reject the configured effective CIDR and exit if it overlaps any non-multitail host address or route; never continue with an ambiguous pool.
- V1 supports IPv4 TCP, UDP, ICMP, and fragmented IPv4 traffic. Design remains IPv6-aware, but IPv6 datapath support is deferred.

## Routing-table naming

V1 configures and manages routes through the numeric table ID only (default `552`). It does not modify `/etc/iproute2/rt_tables`; diagnostics may display a daemon-local label.

## Reload behavior

In v1, changes to existing profiles, profile ordering/removal, and global network settings require a daemon restart to take effect.

The deliberate exception is profile addition: `profiles login` reloads the authoritative YAML and starts profiles newly added since daemon startup, allowing `profiles add` followed immediately by login. It does not apply edits to already running profiles.

Later we can add explicit reload and a `SIGHUP` handler for full reconciliation.
## Management model

V1 has a daemon-owned Unix-domain control socket. `tsmultitail` uses it for interactive profile login/logout and live status/diagnostics. Logout requires explicit confirmation, immediately withdraws the selected profile's routes/DNS/flows, and retains its configuration and state for later re-login. The socket is local-only, root-owned, group-owned by `tsmultitail`, mode `0660`, and authorizes callers using Unix peer credentials. Members of the `tsmultitail` group may perform management operations and read live metadata.

The authoritative YAML config is system-managed at `/etc/tailscale-multitail/config.yaml`. While the daemon runs, authorized CLI config mutations go through its Unix control socket; the daemon performs an atomic, locked rewrite. Initial `config init` before the daemon exists requires root/sudo. Manual editing remains supported for administrators. Most config changes require a daemon restart in v1; `tsmultitail daemon restart` asks the daemon to exit cleanly and its systemd unit starts the new generation. As a usability exception, `profiles login` reads the authoritative YAML and starts profiles newly added since daemon startup, so `profiles add` can be followed immediately by login. Changes to existing profiles, removals, ordering, and global settings still require restart.

## Immediate next step

Complete the Milestone 7 recovery/upgrade matrix, release runbook, and final security review.
