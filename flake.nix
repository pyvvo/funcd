{
  description = "funcd — lightweight serverless platform";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # Lima pinned to a specific nixpkgs revision → reproducible VM tooling for the ADR-0052
    # containerd footprint lane (`just bench-containerd-lima`). Kept on its OWN pin, independent of
    # the rolling nixpkgs above, so a `nix flake update` of the main input never silently changes
    # the Lima version under the bench.
    nixpkgs-lima.url = "github:NixOS/nixpkgs/9ae611a455b90cf061d8f332b977e387bda8e1ca"; # lima 2.1.2
  };

  outputs = { self, nixpkgs, nixpkgs-lima }: let
    supportedSystems = [ "aarch64-darwin" "x86_64-linux" "aarch64-linux" ];
    forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    pkgsFor = system: import nixpkgs { inherit system; };
    # Lima only on macOS (the footprint lane needs a real Linux VM there); sourced from the pinned
    # nixpkgs-lima, installed through the dev shell rather than system-wide.
    limaFor = system: nixpkgs.lib.optionals (pkgsFor system).stdenv.isDarwin
      [ (import nixpkgs-lima { inherit system; }).lima ];
  in {
    devShells = forAllSystems (system: {
      default = (pkgsFor system).mkShellNoCC {
        packages = (with (pkgsFor system); [
          go
          gopls
          just
          git
        ]) ++ limaFor system;
        shellHook = ''
          echo "funcd dev shell — go $(go version | awk '{print $3}')"
        '';
      };
    });
  };
}
