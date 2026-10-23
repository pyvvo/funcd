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
    # FizzBee (Apache-2.0), the model checker of the design specs under docs/design (ADR-0272). Not in nixpkgs: the
    # release tarball is pinned by the SHA-256 GitHub publishes for it. Only its Go checker and its pure-Python parser
    # are kept (the Linux tarball also bundles a 500 MB Python); `fizz` runs the parser on the dev shell's Python.
    fizzbeeFor = system: let
      pkgs = pkgsFor system;
      release = {
        aarch64-darwin = { plat = "macos_arm"; hash = "sha256-IJHQob9azEEBX4ZEy69eyFPtDIgoOn+U1hUTWmyKRBw="; };
        x86_64-linux = { plat = "linux_x86"; hash = "sha256-ih0JAPmtkgACFRkiM3kS4R+eWIovEZYkn+QL91Nw94I="; };
        aarch64-linux = { plat = "linux_arm"; hash = "sha256-vYWDa9umbq39zV9hDvJxd9UduX4p/ccmv4JDFSiHJWo="; };
      }.${system};
      python = pkgs.python314;
    in pkgs.stdenv.mkDerivation rec {
      pname = "fizzbee";
      version = "0.5.4";
      src = pkgs.fetchurl {
        url = "https://github.com/fizzbee-io/fizzbee/releases/download/v${version}/fizzbee-v${version}-${release.plat}.tar.gz";
        inherit (release) hash;
      };
      nativeBuildInputs = pkgs.lib.optionals pkgs.stdenv.isLinux [ pkgs.autoPatchelfHook ];
      dontConfigure = true;
      dontBuild = true;
      dontStrip = true;
      installPhase = ''
        r=parser/parser_bin.runfiles
        lib=$out/libexec/fizzbee
        mkdir -p $out/bin $lib
        cp fizzbee $lib/
        cp -r $r/_main $lib/_main
        cp -r "$r/protobuf+/python" $lib/protobuf
        cp -r "$r/rules_python++pip+pypi_312_antlr4_python3_runtime/site-packages" $lib/antlr4
        cat > $out/bin/fizz <<EOF
        #!${pkgs.bash}/bin/bash
        # fizz [fizzbee flags...] <spec.fizz>: parse the spec into <spec>.json beside it, then model-check it.
        set -euo pipefail
        spec="\''${@: -1}"
        log="\$(mktemp)"
        if ! PYTHONPATH="$lib/protobuf:$lib/antlr4:$lib/_main" ${python}/bin/python3 -s -m parser.main "\$spec" >"\$log" 2>&1; then
          cat "\$log" >&2
          rm -f "\$log"
          echo "fizz: \$spec does not parse" >&2
          exit 1
        fi
        rm -f "\$log"
        exec $lib/fizzbee "\''${@:1:\$#-1}" "\''${spec%.*}.json"
        EOF
        chmod +x $out/bin/fizz
      '';
    };
  in {
    packages = forAllSystems (system: { fizzbee = fizzbeeFor system; });
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
          # PyYAML: the lane tools read their registry and VM YAML (scripts/lanes.yaml, scripts/lima.yaml) with it
          (python314.withPackages (ps: [ ps.fastjsonschema ps.pyyaml ]))
          uv  # the duckdb lane's build runs funcd-bundle with it (scripts/lanes.yaml)
        ]) ++ limaFor system ++ [ (venomFor system) (fizzbeeFor system) ];
        shellHook = ''
          echo "funcd dev shell — go $(go version | awk '{print $3}')"
          # Install the lefthook git hooks (idempotent) so a commit can't drift the tree out of CI-green.
          command -v lefthook >/dev/null 2>&1 && [ -d .git ] && lefthook install >/dev/null 2>&1 || true
        '';
      };
    });
  };
}
