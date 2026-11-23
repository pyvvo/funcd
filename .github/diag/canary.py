"""Diagnostic only (draft PR, not for merge): report who signals the CI step's process group.

Started in the background from the step's shell, so it shares the step's process group. It blocks SIGTERM, SIGINT
and SIGHUP and waits for them with sigwaitinfo, which names the sender; it then prints the sender's parent chain,
the processes by memory, and the kernel log and journal tails.
"""

import signal
import subprocess
import sys

SIGS = [signal.SIGTERM, signal.SIGINT, signal.SIGHUP]


def cmdline(pid: int) -> str:
    try:
        with open(f"/proc/{pid}/cmdline", "rb") as f:
            return f.read().replace(b"\0", b" ").decode(errors="replace").strip()[:240] or "<empty>"
    except OSError:
        return "<gone>"


def chain(pid: int) -> str:
    links = []
    for _ in range(10):
        links.append(f"{pid}:{cmdline(pid)}")
        try:
            with open(f"/proc/{pid}/stat") as f:
                ppid = int(f.read().rsplit(")", 1)[1].split()[1])
        except (OSError, ValueError, IndexError):
            break
        if ppid <= 0:
            break
        pid = ppid
    return " <- ".join(links)


def run(cmd: str) -> None:
    subprocess.run(cmd, shell=True, stdout=sys.stdout, stderr=subprocess.STDOUT, timeout=30)


signal.pthread_sigmask(signal.SIG_BLOCK, SIGS)
print("CANARY armed", flush=True)
while True:
    info = signal.sigwaitinfo(SIGS)
    name = signal.Signals(info.si_signo).name
    print(f"CANARY {name} from pid {info.si_pid} uid {info.si_uid} code {info.si_code}", flush=True)
    print(f"CANARY sender chain: {chain(info.si_pid)}", flush=True)
    run("free -m; cat /proc/pressure/memory")
    run("ps -eo pid,ppid,pgid,sid,stat,etimes,rss,comm --sort=-rss | head -25")
    run("sudo -n dmesg -T | tail -40")
    run("sudo -n journalctl -n 60 --no-pager")
    sys.stdout.flush()
