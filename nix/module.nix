{ config, lib, pkgs, ... }:

let
  cfg = config.services.tailscale-multitail;
  yaml = (pkgs.formats.yaml { }).generate "tailscale-multitail-config.yaml" cfg.settings;
in {
  options.services.tailscale-multitail = {
    enable = lib.mkEnableOption "tailscale-multitail multi-tailnet networking";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.callPackage ./package.nix { };
      defaultText = lib.literalExpression "pkgs.callPackage ./package.nix { }";
      description = "The tailscale-multitail package to run.";
    };

    settings = lib.mkOption {
      type = lib.types.attrs;
      default = {
        version = 1;
        interface = "multitail0";
        routing_table = 552;
        mtu = 1280;
        effective_ipv4_cidr = "10.192.0.0/16";
        profiles = [ ];
      };
      description = ''
        Declarative tailscale-multitail YAML configuration. Configure stable
        profile IDs here; do not use imperative config mutations against the
        Nix store-backed /etc/tailscale-multitail/config.yaml.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = pkgs.stdenv.hostPlatform.system == "x86_64-linux";
        message = "tailscale-multitail v1.0.1 currently ships a Linux x86_64 package only.";
      }
      {
        assertion = !config.services.tailscale.enable;
        message = "Disable services.tailscale: tailscale-multitail cannot run alongside native tailscaled.";
      }
      {
        assertion = config.services.resolved.enable;
        message = "Enable services.resolved: tailscale-multitail uses systemd-resolved for merged DNS.";
      }
    ];

    environment.systemPackages = [ cfg.package ];

    users.groups.tsmultitail = { };

    environment.etc."tailscale-multitail/config.yaml" = {
      source = yaml;
      mode = "0640";
      user = "root";
      group = "tsmultitail";
    };

    systemd.services.tailscale-multitail = {
      description = "Multi-tailnet host networking daemon";
      documentation = [ "https://github.com/kasama-jay/tailscale-multitail" ];

      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" "systemd-resolved.service" ];
      conflicts = [ "tailscaled.service" ];

      serviceConfig = {
        Type = "simple";
        ExecStart = "${cfg.package}/bin/tailscale-multitaild run --host-tun --resolved --socket=/run/tailscale-multitail/control.sock";
        Restart = "on-failure";
        RestartSec = "5s";
        RestartForceExitStatus = "75";
        RestartPreventExitStatus = "1 2";

        User = "root";
        Group = "tsmultitail";
        UMask = "0077";
        RuntimeDirectory = "tailscale-multitail";
        RuntimeDirectoryMode = "0750";
        StateDirectory = "tailscale-multitail";
        StateDirectoryMode = "0700";

        CapabilityBoundingSet = [ "CAP_NET_ADMIN" "CAP_NET_BIND_SERVICE" ];
        AmbientCapabilities = [ "CAP_NET_ADMIN" "CAP_NET_BIND_SERVICE" ];
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectHome = true;
        ProtectSystem = "strict";
        ReadWritePaths = [ "/var/lib/tailscale-multitail" "/run/tailscale-multitail" ];
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        LockPersonality = true;
        RestrictSUIDSGID = true;
      };
    };
  };
}
