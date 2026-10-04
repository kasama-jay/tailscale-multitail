# Exit-node milestone 1: isolated feasibility gate and routing design

This branch does **not** enable exit-node use in `tailscale-multitaild`. Its
integration test exercises the pinned `tailscale.com v1.102.3` public `tsnet`
and LocalAPI interfaces with a channel-backed profile TUN. It does not create
a Linux TUN or modify host routes, DNS, firewall, or the running daemon.

## Run the gate

Provide an auth key for a *new test node* in the exit node's tailnet (prefer a
short-lived, reusable ephemeral test key), the approved exit peer's canonical
Tailscale IPv4 address, and a **public IPv4 UDP echo service that you control**.
The service must reply with the same datagram payload from the same IP and port.
Do not use a third-party reflector or enter keys in CLI arguments, shell
history, or repository files. Start the echo service on a separately hosted
machine; for example, after restricting ingress to the necessary test source
and port, a temporary Python stdlib listener can be run there:

```python
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(("0.0.0.0", 41337))
while True:
    data, addr = s.recvfrom(2048)
    s.sendto(data, addr)
```

On this development host, set these environment variables without persisting
the key in the repository (for example, from a protected secret store):

```sh
export TSMULTITAIL_EXIT_TEST_AUTHKEY=...           # test profile's tailnet
export TSMULTITAIL_EXIT_TEST_PEER_IP=100.x.y.z     # approved exit peer
export TSMULTITAIL_EXIT_TEST_ECHO_ADDR=203.x.y.z:41337 # your public echo server

go test -tags integration ./integration -run '^TestExitNodeCustomTun$' -v -count=1
unset TSMULTITAIL_EXIT_TEST_AUTHKEY
```

The test skips if no auth key is supplied. With a key, missing/invalid peer or
echo input is an error. It waits for the peer in this profile's netmap, requires
`ExitNodeOption` and a stable ID, selects that ID with `LocalClient.EditPrefs`,
waits for `ExitNodeStatus` to report it online, then injects IPv4 UDP with a
random nonce into the profile TUN. It retries during initial WireGuard setup
and requires an exact UDP echo (source IP/port, destination IP/port and nonce)
on that same profile TUN. The profile state lives in a test temp directory.
The test does not use the host's current default route for the probe; a reply
from the public server via this TUN demonstrates the upstream exit-node packet
path. It does **not** validate the daemon mux or OS-wide routing. The gate
requires a live exit peer, permissive ACL, and reachable echo server, so it
cannot be completed by offline CI alone.

## Design for the following implementation milestone (not implemented here)

- Exactly one configured host-wide IPv4 exit selection: profile ID and peer
  stable node ID. Validate approval via that profile's `PeerStatus.ExitNodeOption`
  and report the selected peer's `ExitNodeStatus`, without silently switching to
  another profile. Reapply desired prefs after startup/login; the current
  auth-key login path clears exit prefs. Keep `RouteAll=false` (not accepting
  subnet routes) and clear `ExitNodeIP` when selecting by stable ID.
- Install peer/Service /32 routes (across **all** profiles) before the default
  catch-all. A `0.0.0.0/0` in the current table 552 at priority 5260 would
  capture LAN and transport traffic before the kernel's main table. Instead,
  separate specific-target and exit fallback lookups, with a deliberately
  bounded bypass policy. A candidate rule order: local table (kernel priority
  0), explicit Tailscale transport-bypass mark -> main / fail unreachable,
  table 552 peer/effective /32s, approved local-LAN exceptions, exit fallback
  table containing TUN default **or unreachable**, then normal main/default.
  Never let an exit-configured packet fall through to the ordinary host default.
  Check rule/table collisions with other VPNs before changing host routes.
- Tailscale's Linux transport uses `SO_MARK` via `net/netns` where available
  (pinned version uses `tsconst.LinuxBypassMarkNum`); verify this actually
  covers every embedded profile transport/control socket. A bypass rule must
  precede the catch-all or the tunnel may route its own encrypted packets
  into itself. Do not exempt the entire daemon UID/root: that would also
  exempt unrelated host applications. If marking is unavailable, fail safe or
  use an explicitly verified alternative. Ordinary application traffic must
  not acquire a broad bypass accidentally.
- Host TUN uses the effective-pool NAT source (`10.192.0.1` by default), not
  the selected tsnet self IP. For unmatched public IPv4 destinations the mux
  must rewrite this source to the selected profile's self IP, preserve the
  public destination, and maintain bounded flow/fragment state. Return packets
  from arbitrary internet sources are acceptable **only** if they match an
  outgoing flow; currently `profileLoop` drops sources absent from the peer
  inventory. Verify UDP/TCP, ICMP (including quoted error packets), fragmented
  traffic, and old-flow purge when selection changes or disappears.
- On disconnect, logout, loss of approval, route-update error, or daemon
  restart, retain an unreachable fallback in the exit lookup until exit
  traffic is verified ready again. Route/TUN teardown and service absence
  need an explicit fail-closed policy: if fail-closed behavior after daemon
  exit is required, a transient systemd-managed guard or separate persistent
  firewall/route policy is needed. This is not provided today.
- Decide DNS and LAN scope before advertising this as a full tunnel. Current
  systemd-resolved uses host DNS for public names; LAN and IPv6 may use the
  underlying connection. IPv6 isn't supported by the mux, so a strict
  no-leak promise requires an explicit IPv6/DNS policy, not just an IPv4 route.

Acceptance for the next milestone: verify `ip rule`/`ip route get` for public,
local, peer, DNS, and transport destinations; run an external-IP check and
packet captures; test second-profile reachability, restart, disconnect and
cleanup; and confirm the host never silently falls back when exit is required.
Roll out with console access and a recovery path, not over an SSH session that
would be interrupted by a bad default-route change.
