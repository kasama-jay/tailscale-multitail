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

```sh
TSMULTITAIL_TEST_AUTHKEY_A='tskey-auth-…' \
TSMULTITAIL_TEST_AUTHKEY_B='tskey-auth-…' \
./dist/tailscale-multitail-feasibility_0.5.0_linux_amd64

# Or run the same gate through Go's integration test:
TSMULTITAIL_TEST_AUTHKEY_A='tskey-auth-…' \
TSMULTITAIL_TEST_AUTHKEY_B='tskey-auth-…' \
go test -tags=integration -v ./integration
```

Use distinct, reusable, ephemeral-node auth keys from an isolated test
tailnet. Do not put keys in source control or shell history. The test tailnet
ACL must allow ICMP between the two test nodes.

The next implementation step is a profile wrapper which owns this device,
`tsnet.Server`, and supported LocalAPI inventory watchers. Host TUN creation,
routing, DNS, and address translation remain intentionally unimplemented.
