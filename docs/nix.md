# NixOS installation

The repository exposes a flake package and NixOS module for the v1.0.1 Linux
x86_64 release artifact. The module installs the daemon and CLI, generates the
strict YAML configuration declaratively, creates the management group, and
installs the hardened systemd service.

## Consume as a flake input

Add the input to your system flake:

```nix
inputs.tailscale-multitail.url = "github:kasama-jay/tailscale-multitail/v1.0.1";
```

Import the module and configure it:

```nix
{ inputs, ... }:
{
  imports = [ inputs.tailscale-multitail.nixosModules.default ];

  # Native tailscaled and multitail cannot coexist.
  services.tailscale.enable = false;
  services.resolved.enable = true;

  services.tailscale-multitail = {
    enable = true;

    settings = {
      version = 1;
      interface = "multitail0";
      routing_table = 552;
      mtu = 1280;
      effective_ipv4_cidr = "10.192.0.0/16";

      profiles = [
        {
          # Generate once with `uuidgen`, commit it, and keep it stable.
          id = "4f8d4e75-4642-49e2-a7ea-28f2d2b28b62";
          name = "work";
          hostname = "my-host-work";
        }
      ];
    };
  };

  users.users.alice.extraGroups = [ "tsmultitail" ];
}
```

Apply it with:

```sh
sudo nixos-rebuild switch --flake .#your-host
```

Log out/in after adding group membership. Then authenticate each configured
profile without putting an auth key into the Nix store:

```sh
tsmultitail profiles login work
# Or:
printf '%s' "$TS_AUTHKEY" | tsmultitail profiles login work --auth-key-stdin
```

## Declarative configuration

`/etc/tailscale-multitail/config.yaml` is a Nix store-backed generated file.
Do not use `config init`, `config set`, `profiles add`, `profiles remove`, or
`profiles move` against it. Edit `services.tailscale-multitail.settings` and
run `nixos-rebuild switch` instead.

The normal group-authorized live operations remain available: `status`, login,
logout, and controlled restart. The daemon intentionally requires UID 0 for
configuration writes.

## Direct package use

For the CLI package only:

```sh
nix profile install github:kasama-jay/tailscale-multitail/v1.0.1
```

The package and module currently support `x86_64-linux` because that is the
published v1.0.1 release architecture.
