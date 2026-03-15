{
  description = "kvm-bodge — software KVM dev environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            delve
            gcc
            xdotool
          ];

          # Native libs required by robotgo on Linux (X11 backend).
          buildInputs = with pkgs; lib.optionals stdenv.isLinux [
            libx11
            libxtst
            libxext
            libxinerama
            libxi
            libpng
            xdotool
          ];

          shellHook = ''
            export CGO_ENABLED=1
          '';
        };
      }
    );
}
