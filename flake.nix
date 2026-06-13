{
  description = "funcd — lightweight serverless platform";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }: let
    supportedSystems = [ "aarch64-darwin" "x86_64-linux" "aarch64-linux" ];
    forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    pkgsFor = system: import nixpkgs { inherit system; };
  in {
    devShells = forAllSystems (system: {
      default = (pkgsFor system).mkShellNoCC {
        packages = with (pkgsFor system); [
          go
          gopls
          just
          git
        ];
        shellHook = ''
          echo "funcd dev shell — go $(go version | awk '{print $3}')"
        '';
      };
    });
  };
}
