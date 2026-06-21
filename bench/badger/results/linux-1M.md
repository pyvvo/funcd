# Badger bench — native Linux (arm64) results, 1M keys

Run in a colima container (native aarch64, **not** emulated), GOMAXPROCS=4 (close to the 8-core target),
`-keys 1000000 -funcs 1000 -valsize 256`. These are the **authoritative RSS numbers** — macOS overstates
retained RSS (freed mmap stays resident); Linux reclaims it. Compare with the macOS run in FINDINGS.md.

## profile=lowmem

| scenario | ops | ms | ops/sec | RSS before | RSS after | RSS peak | note |
|---|--:|--:|--:|--:|--:|--:|---|
| bulk-write (WriteBatch) | 1,000,000 | 1426 | 700,982 | 10 | 826 | 797 | |
| get (warm, random) | 200,000 | 729 | 274,338 | 830 | 927 | 990 | 0 miss |
| scan (keys+values) | 1,000,000 | 281 | 3,558,718 | 608 | 608 | 608 | |
| prefix-scan (one fn) | 1,000 | 0 | — | 608 | 608 | — | 1k keys, sub-ms |
| txn-write (batch=16) | 100,000 | 115 | 867,379 | 608 | 684 | 669 | |
| delete (point) | 100,000 | 108 | 922,636 | 684 | 843 | 821 | |
| merge-operator | 100,000 | 319 | 312,622 | 852 | 827 | 846 | |
| **idle-hold (serving, clean)** | — | 2001 | — | 828 | **638** | 748 | post-GC steady |
| subscribe | 20,000 | 112 | 177,821 | 638 | 685 | 675 | 20k/20k delivered |
| backup (full) | — | 462 | — | 685 | 1687 | 1666 | 290 MiB out |
| stream (parallel) | 1,024,896 | 280 | 3,660,342 | 1687 | 1377 | 1627 | |
| DropPrefix (per-fn wipe) | 1 | 90 | — | 1330 | 1281 | 1314 | |
| **idle-hold (after export)** | — | 2002 | — | 1231 | **603** | 1135 | post-GC, reclaimed |
| reopen (cold open) | 1 | 2 | — | 300 | 300 | — | 2 ms |
| **idle-hold (after reopen)** | — | 2001 | — | 300 | **39** | 254 | dormant-store cost |
| get (cold, random) | 200,000 | 569 | 351,377 | 39 | 357 | 358 | |

On disk: dir total **469.7 MiB** · run peak RSS 1687 · final RSS **357**.

## profile=default

| scenario | ops | ms | ops/sec | RSS before | RSS after | RSS peak | note |
|---|--:|--:|--:|--:|--:|--:|---|
| bulk-write (WriteBatch) | 1,000,000 | 1078 | 926,940 | 10 | 843 | 827 | |
| get (warm, random) | 200,000 | 686 | 291,435 | 844 | 804 | 810 | 0 miss |
| scan (keys+values) | 1,000,000 | 320 | 3,125,000 | 804 | 749 | 749 | |
| DropPrefix (per-fn wipe) | 1 | 377 | — | 1418 | 1136 | 1311 | |
| backup (full) | — | 428 | — | 672 | 1580 | 1580 | 291 MiB out |
| **idle-hold (serving, clean)** | — | 2007 | — | 781 | **696** | 740 | post-GC steady |
| **idle-hold (after export)** | — | 2001 | — | 1136 | **362** | 1036 | post-GC, reclaimed |
| reopen (cold open) | 1 | 18 | — | 404 | 404 | — | 18 ms |
| **idle-hold (after reopen)** | — | 2002 | — | 404 | **134** | 338 | dormant-store cost |

On disk: dir total **2224 MiB** (1 GiB value-log preallocation) · run peak RSS 1580 · final RSS **545**.
