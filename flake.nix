{
  description = "Winnow - incremental file organization tool";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f {
        inherit system;
        pkgs = nixpkgs.legacyPackages.${system};
      });
    in
    {
      packages = forAllSystems ({ pkgs, ... }:
        let
          runtimeDeps = with pkgs; [ exiftool file ffmpeg ];

          buildGoModule = pkgs.buildGoModule.override { go = pkgs.go_1_26; };

          winnow-unwrapped = buildGoModule {
            pname = "winnow-unwrapped";
            version = "0.0.1";
            src = ./.;
            vendorHash = "sha256-ocOcgTyD0R1aRmldwd61VmESZOOZoAdYQ2NYRrmIRw0=";
            subPackages = [ "." ];
            meta = {
              description = "Incremental file organization tool";
              mainProgram = "winnow";
            };
          };

          winnow = pkgs.symlinkJoin {
            name = "winnow";
            paths = [ winnow-unwrapped ];
            nativeBuildInputs = [ pkgs.makeWrapper ];
            postBuild = ''
              wrapProgram $out/bin/winnow \
                --prefix PATH : ${pkgs.lib.makeBinPath runtimeDeps}
            '';
            meta = winnow-unwrapped.meta;
          };
        in
        {
          default = winnow;
          winnow = winnow;
        });

      devShells = forAllSystems ({ pkgs, ... }: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_26
            gopls
            gotools
            gofumpt
            golangci-lint
            delve
            exiftool
            file
            ffmpeg
            sqlite
          ];
        };
      });
    };
}
