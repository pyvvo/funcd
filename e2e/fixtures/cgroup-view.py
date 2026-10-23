"""Run in a worker by the duckdb lane (ADR-0264): prints the worker's cgroup view, its own cgroup's memory and CPU
limits, and whether a write under /sys/fs/cgroup is refused."""

with open("/proc/self/cgroup") as f:
    cgroup = f.read().strip()
with open("/sys/fs/cgroup/memory.max") as f:
    memory_max = f.read().strip()
with open("/sys/fs/cgroup/cpu.max") as f:
    cpu_max = f.read().strip()
try:
    with open("/sys/fs/cgroup/memory.max", "w") as f:
        f.write("max")
    write = "allowed"
except OSError as e:
    write = f"refused({e.errno})"
print(f"CGROUP={cgroup} MEMORY_MAX={memory_max} CPU_MAX={cpu_max.replace(' ', '/')} WRITE={write}")
