#!/bin/sh
# install-funcdctl.sh: install funcdctl from a funcd release on macOS or Linux, in its dev build, which also runs
# Functions on this machine with `funcdctl dev`. The download is checked against the release's SHA256SUMS first. The
# docs site serves this script as https://pyvvo.github.io/funcd/install.sh (.github/workflows/docs-site.yml):
#
#   curl -fsSL https://pyvvo.github.io/funcd/install.sh | sh
#
# FUNCDCTL_VERSION installs a given release (v0.41.0) instead of the latest one. FUNCDCTL_INSTALL_DIR is where funcdctl
# goes, /usr/local/bin by default, through sudo when it is not writable. FUNCDCTL_RELEASES is the base URL of the
# releases, GitHub's by default; the tests point it at a local server.
set -eu

releases=${FUNCDCTL_RELEASES:-https://github.com/pyvvo/funcd/releases}
version=${FUNCDCTL_VERSION:-latest}
dir=${FUNCDCTL_INSTALL_DIR:-/usr/local/bin}

fail() {
  echo "install-funcdctl: $*" >&2
  exit 1
}

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "funcdctl runs on macOS and Linux, not $(uname -s)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "funcdctl is built for amd64 and arm64, not $(uname -m)" ;;
esac

if [ "$version" = latest ]; then
  base=$releases/latest/download
else
  base=$releases/download/$version
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || fail "found no release at $base"
file=$(grep -o "funcdctl-dev-v[0-9.]*-$os-$arch\$" "$tmp/SHA256SUMS") || fail "the release has no dev build for $os-$arch"
release=${file#funcdctl-dev-}
release=${release%-"$os-$arch"}

echo "Downloading funcdctl $release for $os-$arch"
curl -fL --progress-bar "$base/$file" -o "$tmp/$file" || fail "could not download $base/$file"

if command -v sha256sum >/dev/null 2>&1; then
  check='sha256sum -c'
else
  check='shasum -a 256 -c'
fi
(cd "$tmp" && grep " $file\$" SHA256SUMS | $check >/dev/null 2>&1) || fail "$file does not match its checksum in SHA256SUMS"

if mkdir -p "$dir" 2>/dev/null && [ -w "$dir" ]; then
  install -m 0755 "$tmp/$file" "$dir/funcdctl"
else
  echo "Installing into $dir needs sudo"
  sudo mkdir -p "$dir"
  sudo install -m 0755 "$tmp/$file" "$dir/funcdctl"
fi

echo "Installed funcdctl $release in $dir/funcdctl"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH to run funcdctl" ;;
esac
