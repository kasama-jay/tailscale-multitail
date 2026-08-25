# tailscale-multitail

This branch implements the **Milestone 0.5 feasibility gate**, rather than a
privileged host daemon. It pins upstream `tailscale.com` and supplies a bounded
channel-backed `tun.Device` suitable for placing a packet mux in front of each
embedded `tsnet.Server`.

## Build

Build the Linux amd64 feasibility binary with its release version embedded:

```sh
make release VERSION=0.5.0
./dist/tailscale-multitail-feasibility_0.5.0_linux_amd64 --version
```

## Checks

```sh
go test ./...
```

The real-tailnet feasibility check is deliberately opt-in. It creates two
short-lived nodes with separate state directories, injects IPv4 ICMP packets
into each custom TUN, and verifies that they emerge from the remote profile's
custom TUN. It verifies `LocalClient.Status` peer inventory (stable ID,
canonical IP, and MagicDNS name), `QueryDNS`, `GetServices`, and a
`WatchIPNBus` peer-add event. `GetServices` validates the supported inventory
API; an actual Service advertisement additionally needs a tagged node and
Tailscale service-policy approval.

To test a packet originating from a physically separate node, supply the
IPv4 Tailscale address of a normal node in the **same** test tailnet. That
node must permit and answer ICMP:

```sh
./tailscale-multitail-feasibility --external-peer 100.x.y.z
```

To validate DNS isolation and Services inventory across two tailnets, set
`TSMULTITAIL_TEST_AUTHKEY_OTHER` to a reusable auth key from a different
MagicDNS tailnet and provide a normal ICMP-capable node plus a known Service
in that other tailnet:

```sh
TSMULTITAIL_TEST_AUTHKEY_A='…' \
TSMULTITAIL_TEST_AUTHKEY_B='…' \
TSMULTITAIL_TEST_AUTHKEY_OTHER='…' \
./tailscale-multitail-feasibility \
  --other-external-peer 100.x.y.z \
  --service-fqdn service.example.ts.net \
  --service-ip 100.x.y.z
```

The gate queries the Service name through the other profile and confirms the
public `GetServices` inventory reports the expected IPv4 Service address. It
does not issue an HTTP request through `tsnet.HTTPClient`: with a custom TUN,
that client has no host-side mux/netstack loop to return response packets yet.

```sh
TSMULTITAIL_TEST_AUTHKEY_A='tskey-auth-…' \
TSMULTITAIL_TEST_AUTHKEY_B='tskey-auth-…' \
./dist/tailscale-multitail-feasibility_0.5.0_linux_amd64

# Or run the same gate through Go's integration test:
TSMULTITAIL_TEST_AUTHKEY_A='tskey-auth-…' \
TSMULTITAIL_TEST_AUTHKEY_B='tskey-auth-…' \
go test -tags=integration -v ./integration
```

Use distinct, reusable, ephemeral-node auth keys from isolated test
tailnets. Do not put keys in source control or shell history. The test
tailnets must allow the requested ICMP traffic.

## Milestone 1 runtime skeleton

`tailscale-multitaild` starts one upstream `tsnet.Server` per configured
profile, each with its derived state directory and a channel-backed internal
TUN. It reads enrollment keys only from
`TAILSCALE_AUTH_KEY_<UPPERCASE_PROFILE_NAME>`; it never writes keys to config
or runtime status.

For an unprivileged test run, use explicit temporary config and state paths:

```sh
tailscale-multitaild run --config /tmp/config.yaml --state-root /tmp/state --once
```

`--once` starts the configured profiles, prints their LocalAPI-derived status
as JSON, then exits. Production host-TUN creation, routes, DNS, and address
translation remain subsequent milestones.
