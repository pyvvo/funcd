#!/usr/bin/env bash
# lane-reset.sh: reset the shared lane VM of `just lima-example-all` after a lane, as root in the guest (ADR-0230
# Decision 4), so the next lane starts on a clean platform. Idempotent; exits 0 on a clean VM and 1 when a step fails.
# It removes /var/lib/funcd/data whole, the lane's containerd root included, because every lane config sets dataDir:
# /var/lib/funcd/data (held by scripts/lanes_test.go), and keeps /var/lib/funcd/bin, the runtime `funcd install` laid
# down. scripts/lane-leakcheck.sh then counts what is left.
set -u
data=/var/lib/funcd/data
rc=0
t0=$(date +%s%N)
fail() { echo "lane-reset: $*" >&2; rc=1; }

# SIGKILL, not a stop: the reset discards the platform anyway. reset-failed then frees the unit name for the next
# lane's systemd-run, once the unit has left the active states.
systemctl kill -s KILL funcd-demo 2>/dev/null
for _ in $(seq 1 100); do
  case $(systemctl show -p ActiveState --value funcd-demo) in
    active | activating | deactivating | reloading) sleep 0.1 ;;
    *) break ;;
  esac
done
systemctl reset-failed funcd-demo 2>/dev/null
for _ in $(seq 1 50); do
  [ "$(systemctl show -p LoadState --value funcd-demo)" = not-found ] && break
  sleep 0.1
done
[ "$(systemctl show -p LoadState --value funcd-demo)" = not-found ] || fail "the unit funcd-demo is still loaded"

# The workers run outside the unit, in containerd's cgroups /<namespace>/<id>, under /sys/fs/cgroup/funcd-*.
for cg in /sys/fs/cgroup/funcd-*/; do
  [ -d "$cg" ] && { echo 1 >"${cg}cgroup.kill" || fail "cannot kill the workers of $cg"; }
done
for _ in $(seq 1 50); do
  grep -qsx 'populated 1' /sys/fs/cgroup/funcd-*/cgroup.events || break
  sleep 0.1
done
for cg in /sys/fs/cgroup/funcd-*/; do
  [ -d "$cg" ] || continue
  find "$cg" -depth -type d -exec rmdir {} \; 2>/dev/null
  [ ! -d "$cg" ] || fail "cannot remove the cgroup $cg"
done

# The namespaces funcd pins with a bind mount under <dataDir>/run/netns, then the task mounts the killed containerd
# left (the workers' rootfs), deepest first.
for pin in "$data"/run/netns/*; do
  [ -e "$pin" ] || continue
  umount -l "$pin" 2>/dev/null
  rm -f "$pin" || fail "cannot remove the netns pin $pin"
done
while read -r mnt; do
  umount -l "$mnt" || fail "cannot unmount $mnt"
done < <(awk '$2 ~ "^/var/lib/funcd/" { print $2 }' /proc/self/mounts | sort -r)

# crun's state of each worker, under /run/containerd/runc/<namespace>/<id>, outside $data: left there, the next
# platform's first create of the same worker id fails with "already exists" (#1291). A real host's reboot clears /run.
rm -rf /run/containerd/runc/funcd-* || fail "cannot remove the runtime state under /run/containerd/runc"

# The worker network: the bridge, the CNI IP leases and results, the CNI iptables lines, and funcd's nftables tables.
if ip link show funcd0 >/dev/null 2>&1; then ip link delete funcd0 || fail "cannot delete the link funcd0"; fi
rm -rf /var/lib/cni || fail "cannot remove /var/lib/cni"
if iptables-save 2>/dev/null | grep -q CNI; then
  iptables-save | grep -v CNI | iptables-restore || fail "cannot remove the CNI iptables lines"
fi
for table in "inet funcd_egress" "bridge funcd_lateral"; do
  read -r family name <<<"$table"
  if nft list table "$family" "$name" >/dev/null 2>&1; then
    nft delete table "$family" "$name" || fail "cannot delete the nftables table $table"
  fi
done

# The journal: a suite reads the funcd-demo lines, so the next lane must see only its own. --sync first, so the
# killed unit's last lines are written before the rotation.
journalctl --sync && journalctl --rotate && journalctl --vacuum-time=1s >/dev/null 2>&1 ||
  fail "cannot rotate and vacuum the journal"

rm -rf "$data" /opt/lane /etc/funcd/tokens || fail "cannot remove $data, /opt/lane or /etc/funcd/tokens"
echo "lane-reset: took $((($(date +%s%N) - t0) / 1000000)) ms"
exit "$rc"
