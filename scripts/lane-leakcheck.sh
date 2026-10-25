#!/usr/bin/env bash
# lane-leakcheck.sh: count what a lane left on the shared lane VM after scripts/lane-reset.sh, as root in the guest
# (ADR-0230 Decision 4). Prints each count and exits 1 when any is not zero: `just lima-example-all` then fails the
# lane that just ran and stops.
set -u
data=/var/lib/funcd/data
command -v nft >/dev/null || { echo "lane-leakcheck: no nft to count the nftables tables with" >&2; exit 1; }
exists() { if [ -e "$1" ]; then echo 1; else echo 0; fi; }
counts=(
  "cgroups=$(find /sys/fs/cgroup/funcd-* -type d 2>/dev/null | wc -l)"
  "task-mounts=$(awk '$2 ~ "^/var/lib/funcd/"' /proc/self/mounts | wc -l)"
  "netns-pins=$(find "$data/run/netns" -mindepth 1 2>/dev/null | wc -l)"
  "runc-state=$(ls -d /run/containerd/runc/funcd-* 2>/dev/null | wc -l)"
  "funcd0=$(ip -o link show funcd0 2>/dev/null | wc -l)"
  "cni-leases=$(find /var/lib/cni/networks/funcd -maxdepth 1 -type f -name '[0-9]*' 2>/dev/null | wc -l)"
  "nft-tables=$(nft list tables | awk '$3 ~ /^funcd_/' | wc -l)"
  "data-dir=$(exists "$data")"
  "opt-lane=$(exists /opt/lane)"
  "tokens=$(exists /etc/funcd/tokens)"
  "journal-lines=$(journalctl -u funcd-demo -q --no-pager -o cat | wc -l)"
)
echo "lane-leakcheck: ${counts[*]}"
leaks=()
for c in "${counts[@]}"; do
  [ "${c#*=}" = 0 ] || leaks+=("$c")
done
[ ${#leaks[@]} -eq 0 ] || { echo "lane-leakcheck: left behind: ${leaks[*]}" >&2; exit 1; }
