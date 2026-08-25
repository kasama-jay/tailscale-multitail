# tailscale-multitail

This branch implements the **Milestone 0.5 feasibility gate**, rather than a
privileged host daemon. It pins upstream `tailscale.com` and supplies a bounded
channel-backed `tun.Device` suitable for placing a packet mux in front of each
embedded `tsnet.Server`.

## Checks

```sh
go test ./...
```

The real-tailnet feasibility check is deliberately opt-in. It creates two
short-lived nodes with separate state directories, injects IPv4 ICMP packets
into each custom TUN, and verifies that they emerge from the remote profile's
custom TUN. It also exercises the supported `LocalClient.Status` and
`WatchIPNBus` interfaces.

```sh
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
