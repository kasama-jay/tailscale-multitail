{ stdenvNoCC, fetchurl }:

stdenvNoCC.mkDerivation (finalAttrs: {
  pname = "tailscale-multitail";
  version = "1.0.1";

  src = fetchurl {
    url = "https://github.com/kasama-jay/tailscale-multitail/releases/download/v${finalAttrs.version}/tailscale-multitail_${finalAttrs.version}_linux_amd64.tar.gz";
    hash = "sha256-dE65VKSZYQz9VozGXbek2Bwy6Ac56KT/WQ0IXLOj6wo=";
  };

  dontConfigure = true;
  dontBuild = true;

  unpackPhase = ''
    tar -xzf "$src"
  '';

  sourceRoot = "tailscale-multitail_${finalAttrs.version}_linux_amd64";

  installPhase = ''
    install -Dm755 tailscale-multitaild "$out/bin/tailscale-multitaild"
    install -Dm755 tsmultitail "$out/bin/tsmultitail"
    install -Dm644 tailscale-multitail.service "$out/lib/systemd/system/tailscale-multitail.service"
    install -Dm644 README.md "$out/share/doc/tailscale-multitail/README.md"
    install -Dm644 INSTALL.md "$out/share/doc/tailscale-multitail/INSTALL.md"
  '';

  meta = {
    description = "Multi-tailnet host networking daemon";
    homepage = "https://github.com/kasama-jay/tailscale-multitail";
    platforms = [ "x86_64-linux" ];
    mainProgram = "tsmultitail";
  };
})
