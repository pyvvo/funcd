package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// processRSSMB returns the OS-reported resident set size in MiB (VmRSS on Linux,
// `ps -o rss=` on macOS) — the memory number that decides whether an embedded JS
// engine fits funcd's RAM budget.
func processRSSMB() float64 {
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				if f := strings.Fields(line); len(f) >= 2 {
					if kb, err := strconv.ParseFloat(f[1], 64); err == nil {
						return kb / 1024
					}
				}
			}
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err == nil {
		if kb, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
			return kb / 1024
		}
	}
	return 0
}
