# Operations runbook

## Install and upgrade

Use the release archive's checksum and bundled `install.sh`. The daemon must
not run alongside native `tailscaled`; retain independent console or SSH access
before stopping it.

For an upgrade, stop the service, install both release binaries and the unit,
reload systemd, then start it:

```sh
sudo systemctl stop tailscale-multitail
sudo install -m755 tailscale-multitaild tsmultitail /usr/bin/
sudo install -m644 tailscale-multitail.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl start tailscale-multitail
sudo tsmultitail status
```

Use persistent Tailscale identities for restart-persistent profiles. An
**ephemeral** auth key intentionally creates an identity that requires login
again after daemon restart.

## Rollback

Keep the previous release archive until the new daemon has passed connectivity
checks. To roll back:

```sh
sudo systemctl stop tailscale-multitail
sudo install -m755 /path/to/previous/tailscale-multitaild /path/to/previous/tsmultitail /usr/bin/
sudo install -m644 /path/to/previous/tailscale-multitail.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl start tailscale-multitail
```

To return to native Tailscale instead, stop and disable multitail, remove its
routing state by allowing the service to exit cleanly, then restore/start
`tailscaled`. Do not overlap the two daemons.

## Routine checks

```sh
tsmultitail validate
tsmultitail doctor
tsmultitail status
journalctl -u tailscale-multitail -f
```

`status` is the machine-readable v1 metrics surface. Watch `drops`, capacity
drop counters, `rate_limited_errors`, and current flow/fragment counts. Nonzero
purge counters after logout/degradation are expected: they confirm state for
withdrawn profiles was removed.

## Recovery validation matrix

Run this on a non-critical test host before upgrading a production-like host:

1. Start two profiles; verify effective and raw ICMP/TCP to an ordinary peer.
2. Verify MagicDNS A/PTR and a known-good Tailscale Service HTTPS endpoint.
3. Add a third profile and immediately authenticate it with `profiles login`.
4. Log out one profile; confirm its targets/routes/DNS leases withdraw while
   the other profile remains reachable, and inspect status purge counters.
5. Restore the logged-out profile; verify its inventory and connectivity return.
6. Perform `tsmultitail daemon restart`; verify TUN, routes, resolved link
   settings, DNS, and connectivity after startup.
7. Stop the service; verify `multitail0`, table-552 routes, rule priority 5260,
   and `/run/tailscale-multitail/control.sock` are gone.
8. Upgrade to the candidate binary, repeat checks 1–6, then roll back and
   repeat checks 1–2.

Capture `tsmultitail status`, `resolvectl status multitail0`, `ip rule show`,
and the service journal for any failure report.
