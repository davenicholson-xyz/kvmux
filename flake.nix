{
  description = "kvmux — software KVM dev environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in {
        packages.default = pkgs.buildGoModule {
          pname = "kvmux-server";
          version = "0.1.0";
          src = ./.;
          subPackages = [ "cmd/kvmux-server" ];
          vendorHash = nixpkgs.lib.fakeHash;
          CGO_ENABLED = "1";
          nativeBuildInputs = [ pkgs.pkg-config ];
          buildInputs = with pkgs; [ libx11 libxtst libxext libxinerama libxi libpng ];
        };

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
