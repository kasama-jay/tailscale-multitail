# tailscale-multitail beta installation

This archive is for **Linux x86_64**. It is a beta: use a non-critical host and
retain independent console or SSH access while evaluating it.

1. Download both the release archive and its `.sha256` file, then verify:

   ```sh
   # Substitute the version shown in the release asset name.
   sha256sum -c tailscale-multitail_<VERSION>_linux_amd64.tar.gz.sha256
   tar -xzf tailscale-multitail_<VERSION>_linux_amd64.tar.gz
   cd tailscale-multitail_<VERSION>_linux_amd64
   sudo ./install.sh
   ```

2. `install.sh` verifies the archive contents, creates the `tsmultitail` group,
   installs binaries and the systemd unit, and writes a default config if none
   exists. It refuses to proceed while native `tailscaled` or `tailscale0` is
   active. Stop native Tailscale only after confirming an independent access
   path.

3. Permit a local operator, then start a new login session so group membership
   applies:

   ```sh
   sudo usermod -aG tsmultitail "$USER"
   ```

4. Add a profile, validate, and start the service:

   ```sh
   sudo tsmultitail profiles add work --hostname myhost-work
   sudo tsmultitail validate
   sudo tsmultitail doctor
   sudo systemctl enable --now tailscale-multitail
   ```

5. Authenticate without putting the auth key in shell history or argv:

   ```sh
   read -rsp 'Auth key: ' KEY; echo
   printf '%s' "$KEY" | tsmultitail profiles login work --auth-key-stdin
   unset KEY
   ```

Monitor with `tsmultitail status` and `journalctl -u tailscale-multitail -f`.
Rollback: `sudo systemctl disable --now tailscale-multitail`, then restore and
start your normal `tailscaled` installation.
