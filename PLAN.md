# PLAN

## Project

**tailscale-multitail**

Primary daemon binary: **`tailscale-multitaild`**

Linux-only daemon for connecting one host to multiple Tailscale tailnets at the same time using upstream `tsnet`, one embedded profile per tailnet, one host-visible TUN, one local DNS service, and a packet mux/translation layer.

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

## Major technical workstreams

1. **Requirements freeze**
   - finalize semantics for profile ordering, ambiguity handling, DNS, and config management
2. **Process/config design**
   - daemon CLI shape
   - versioned, strict config/state schema
   - system YAML file layout and state-directory ownership
   - Unix control socket protocol and peer-credential authorization
   - config rewrite/update semantics
   - routing table configuration
3. **Profile engine abstraction**
   - wrapper around upstream `tsnet.Server`
   - LocalAPI watcher/status interface
4. **Host TUN design**
   - Linux TUN creation
   - address and route reconciliation
5. **Packet mux/NAT design**
   - effective-IP translation
   - raw-IP profile lookup path
   - IPv4 TCP, UDP, ICMP, and bounded fragmentation/reassembly handling; expire idle state before dropping new traffic under resource pressure (v1 defaults: TCP 5m, UDP 60s, ICMP/fragments 30s; caps: 65,536 flows and 8,192 fragment entries)
   - checksum handling
6. **DNS design**
   - local authoritative records
   - UDP and TCP DNS on port 53 with EDNS (1232-byte advertised UDP payload); no daemon DNSSEC validation
   - search-order semantics
   - forwarding behavior
   - systemd-resolved per-link DNS/domain configuration and reconciliation without claiming the default `~.` DNS route; install unique tailnet suffixes as ordered host search domains
7. **Observability design**
   - status output
   - collision/ambiguity diagnostics (not default warnings; ordered first-match is expected behavior)
   - per-profile health
   - no per-packet logging by default; aggregate counters and rate-limited errors, with explicit temporary debug mode for flow-level detail
8. **Security review**
   - tailnet isolation risks
   - privileges
   - local control API exposure

## Proposed milestone sequence

### Milestone 0 — design only
- write PLAN.md
- write architecture doc
- capture assumptions and tradeoffs

### Milestone 0.5 — tsnet feasibility gate
- pin the exact upstream Tailscale module version
- prove a channel-backed custom `tun.Device` can carry host-TUN-to-profile and reverse packets with a real remote peer
- verify supported public LocalClient APIs provide required peer, Service, DNS, and netmap-change inventory
- verify profile-scoped DNS querying and the intended inbound packet path
- establish integration tests before depending on this behavior

### Milestone 1 — profile/runtime skeleton
- create daemon shell
- create profile abstraction around upstream `tsnet`
- spin up multiple profiles with separate state dirs
- inspect peer/status data

### Milestone 2 — in-memory model
- maintain aggregate peer/service/routing database
- maintain ordered profile index
- track canonical IP ownership and collisions

### Milestone 3 — DNS MVP
- local DNS service
- merged MagicDNS FQDN resolution
- ordered short-name search
- MagicDNS always returns effective IPs
- tailnet-zone forwarding via the authoritative selected profile when local MagicDNS inventory cannot answer; rewrite any mapped canonical direct-peer/Service A record to its effective IP while preserving unrelated public records and CNAME structure, with TTL `min(upstream, 30s)`; public/default DNS remains with the existing host resolver

### Milestone 4 — host TUN MVP
- create Linux TUN
- create dedicated routing table and `ip rule` integration
- install minimal host routes in configured table
- add per-known-direct-target canonical IP routes
- add per-effective-IP routes
- inject/receive packets

### Milestone 5 — effective-IP datapath
- allocate effective IPs
- direct peer/service translation
- host connectivity to effective IPs

### Milestone 6 — raw canonical IP routing
- ordered per-profile lookup for canonical Tailscale IP destinations
- apply to both peers and Services IPs
- add conntrack/state for reply handling
- status/diagnostics for chosen profile

### Milestone 7 — hardening and deployment
- per-profile degradation: withdraw a failed profile's routes/DNS/flows while retaining other profiles; retry transient engine failures with backoff, but never automatically re-login an explicitly logged-out profile
- resilience
- clean restart behavior
- metrics/logging
- recovery tooling
- hardened systemd unit with runtime-directory/socket ownership, ordering with network and systemd-resolved, least required capabilities, and exit-status policy: controlled CLI restart uses a documented restart status; permanent config/host-conflict failures do not loop; transient runtime failures retry with rate limits

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

In v1, config changes require a daemon restart to take effect.

Later we can add:
- explicit reload support
- a `SIGHUP` handler for hot-reload

## Management model

V1 has a daemon-owned Unix-domain control socket. `tsmultitail` uses it for interactive profile login/logout and live status/diagnostics. Logout requires explicit confirmation, immediately withdraws the selected profile's routes/DNS/flows, and retains its configuration and state for later re-login. The socket is local-only, root-owned, group-owned by `tsmultitail`, mode `0660`, and authorizes callers using Unix peer credentials. Members of the `tsmultitail` group may perform management operations and read live metadata.

The authoritative YAML config is system-managed at `/etc/tailscale-multitail/config.yaml`. While the daemon runs, authorized CLI config mutations go through its Unix control socket; the daemon performs an atomic, locked rewrite. Initial `config init` before the daemon exists requires root/sudo. Manual editing remains supported for administrators. Config changes still require a daemon restart in v1; `tsmultitail daemon restart` asks the daemon to exit cleanly and its systemd unit restarts it into the new generation. Login targets only a profile active in the running daemon, so a newly added profile must be restarted into service before login.

## Immediate next step

Refine the architecture document into concrete subsystems, data models, and packet-routing semantics before writing code.
