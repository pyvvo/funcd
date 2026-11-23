"""Diagnostic only (draft PR, not for merge): sample the runner's memory every 2 s during the ci step.

Prints a MEMMAX line when the peak of used memory (MemTotal - MemAvailable) grows by 256 MB, and on SIGINT or SIGTERM
a summary: the peak, the peak while a Go test binary runs, the peak swap, and the processes with the largest peak
RSS, each by name and pid.
"""

import os
import signal
import sys
import time


def meminfo():
    m = {}
    with open("/proc/meminfo") as f:
        for line in f:
            k, v = line.split(":", 1)
            m[k] = int(v.split()[0]) // 1024
    return m["MemTotal"] - m["MemAvailable"], m["SwapTotal"] - m["SwapFree"]


def processes():
    out = []
    for pid in os.listdir("/proc"):
        if not pid.isdigit():
            continue
        try:
            with open(f"/proc/{pid}/status") as f:
                name, rss = "", 0
                for line in f:
                    if line.startswith("Name:"):
                        name = line.split()[1]
                    elif line.startswith("VmRSS:"):
                        rss = int(line.split()[1]) // 1024
            out.append((rss, f"{name}[{pid}]"))
        except OSError:
            pass
    return out


peak, printed, peak_swap, test_peak, per = 0, 0, 0, 0, {}
t0 = time.time()


def summary(*_):
    top = sorted(per.items(), key=lambda kv: -kv[1][0])[:12]
    print(f"MEMSUM peak_used={peak}MB peak_used_while_testing={test_peak}MB peak_swap={peak_swap}MB after {time.time() - t0:.0f}s", flush=True)
    for name, (rss, at) in top:
        print(f"MEMSUM   {name:28s} peak_rss={rss}MB at +{at:.0f}s", flush=True)
    sys.exit(0)


signal.signal(signal.SIGINT, summary)
signal.signal(signal.SIGTERM, summary)
while True:
    used, swap = meminfo()
    procs = processes()
    now = time.time() - t0
    for rss, name in procs:
        if rss > per.get(name, (0, 0))[0]:
            per[name] = (rss, now)
    peak_swap = max(peak_swap, swap)
    if any(name.split("[")[0].endswith(".test") for _, name in procs):
        test_peak = max(test_peak, used)
    peak = max(peak, used)
    if peak >= printed + 256:
        printed = peak
        top = " ".join(f"{n}={r}MB" for r, n in sorted(procs, reverse=True)[:4])
        print(f"MEMMAX +{now:.0f}s used={used}MB swap={swap}MB top: {top}", flush=True)
    time.sleep(2)
