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
    # OVH Venom (Apache-2.0), the declarative e2e runner for the containerd example lanes (ADR-0077).
    # Not in nixpkgs → pinned here via buildGoModule. Cross-platform (unlike Lima), so it is on every
    # supported system's dev shell. Bumped deliberately via this edit, like nixpkgs-lima.
    venomFor = system: (pkgsFor system).buildGoModule rec {
      pname = "venom";
      version = "1.3.0";
      src = (pkgsFor system).fetchFromGitHub {
        owner = "ovh";
        repo = "venom";
        rev = "v${version}";
        hash = "sha256-MyQMmX8R96hsbyJCm/n4GtNL9P6bQwkSSYe1tHmtyT0=";
      };
      vendorHash = "sha256-0stYt58RqHv/rENIFudHdfyxbpnvlfIL7UdWA+nfaIY=";
      subPackages = [ "cmd/venom" ];
      # set the version Venom otherwise leaves as "snapshot" (it injects it via ldflags at release-build)
      ldflags = [ "-s" "-w" "-X github.com/ovh/venom.Version=v${version}" ];
      doCheck = false; # upstream tests need network/containers; we pin the already-proven-green binary
    };
  in {
    devShells = forAllSystems (system: {
      default = (pkgsFor system).mkShellNoCC {
        packages = (with (pkgsFor system); [
          go
          gopls
          just
          git
          lefthook  # git hooks manager (gofmt gate — see lefthook.yml); installed by the shellHook
          act  # run GitHub Actions locally (nektos/act) — needs a docker daemon (colima on macOS)
          # the Python shim's interpreter and runtime dep (ADR-0049/0123), 3.14 for the pool host (ADR-0050)
          (python314.withPackages (ps: [ ps.fastjsonschema ]))
        ]) ++ limaFor system ++ [ (venomFor system) ];
        shellHook = ''
          echo "funcd dev shell — go $(go version | awk '{print $3}')"
          # Install the lefthook git hooks (idempotent) so a commit can't drift the tree out of CI-green.
          command -v lefthook >/dev/null 2>&1 && [ -d .git ] && lefthook install >/dev/null 2>&1 || true
        '';
      };
    });
  };
}
