{
  description = "tailscale-multitail: multi-tailnet host networking for Linux";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      packages = forAllSystems (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          default = pkgs.callPackage ./nix/package.nix { };
          tailscale-multitail = pkgs.callPackage ./nix/package.nix { };
        });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/tsmultitail";
        };
      });

      nixosModules.default = import ./nix/module.nix;
      nixosModules.tailscale-multitail = self.nixosModules.default;
    };
}
