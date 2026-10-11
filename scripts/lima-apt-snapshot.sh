#!/bin/bash
# lima-apt-snapshot.sh: point apt in a lane or test VM at one Debian snapshot, as the `mode: dependency` provision step
# of scripts/lima.yaml and scripts/lima-linux-tests.yaml, which Lima runs before it installs its own packages. Every
# apt-get in the VM, Lima's and the provision scripts', then installs the same versions on any host on any day, and the
# image's daily apt timers are off, so nothing upgrades a package during a run. The image's sources
# (/etc/apt/sources.list.d/debian.sources) read their mirrors from /etc/apt/mirrors/*.list. Move the timestamp only
# with the versions scripts/lane-provision.sh and scripts/lima-linux-tests.yaml install.
set -euo pipefail
snapshot=20261011T144745Z
for archive in debian debian-security; do
  test -f "/etc/apt/mirrors/$archive.list"
  echo "https://snapshot.debian.org/archive/$archive/$snapshot" >"/etc/apt/mirrors/$archive.list"
done
# The snapshot serves each Release file as it was published, past its Valid-Until.
echo 'Acquire::Check-Valid-Until "false";' >/etc/apt/apt.conf.d/99snapshot
systemctl disable --now apt-daily.timer apt-daily-upgrade.timer
