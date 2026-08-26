#!/bin/sh
# Install a tailscale-multitail Linux amd64 release archive. Run as root.
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root: sudo ./install.sh" >&2
  exit 1
fi
case "$(uname -s):$(uname -m)" in
  Linux:x86_64) ;;
  *) echo "this release supports Linux x86_64 only" >&2; exit 1 ;;
esac
for f in tailscale-multitaild tsmultitail tailscale-multitail.service SHA256SUMS; do
  [ -f "$f" ] || { echo "missing $f; run from the extracted release directory" >&2; exit 1; }
done
sha256sum -c SHA256SUMS
if systemctl is-active --quiet tailscaled.service || ip link show tailscale0 >/dev/null 2>&1; then
  cat >&2 <<'EOF'
Native tailscaled/tailscale0 is active. Do not run it alongside multitail.
Stop it only after ensuring you have an independent console/SSH path:
  systemctl disable --now tailscaled
Then rerun this installer.
EOF
  exit 1
fi
getent group tsmultitail >/dev/null || groupadd --system tsmultitail
install -m 0755 tailscale-multitaild tsmultitail /usr/bin/
install -m 0644 tailscale-multitail.service /etc/systemd/system/tailscale-multitail.service
install -d -o root -g tsmultitail -m 0750 /etc/tailscale-multitail /var/lib/tailscale-multitail
systemctl daemon-reload
if [ ! -e /etc/tailscale-multitail/config.yaml ]; then
  tsmultitail config init
fi
cat <<'EOF'
Installed. Before enabling the daemon:
  1. Add an administrator to the local management group, then re-login:
       usermod -aG tsmultitail <user>
  2. Add profiles and validate configuration:
       tsmultitail profiles add work --hostname <unique-hostname>
       tsmultitail validate
       tsmultitail doctor
  3. Enable and authenticate:
       systemctl enable --now tailscale-multitail
       read -rsp 'Auth key: ' KEY; echo
       printf '%s' "$KEY" | tsmultitail profiles login work --auth-key-stdin
       unset KEY

Use an independent console/SSH path during initial testing. See README.md.
EOF
