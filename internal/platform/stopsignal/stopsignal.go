// Package stopsignal names the signals that stop a funcd process, such as the daemon or `funcdctl dev`, gracefully.
package stopsignal

import (
	"os"
	"os/signal"
	"syscall"
)

// Signals are the signals that stop a funcd process gracefully. A hangup (a closed terminal or a dropped SSH session)
// is one: it does not reach the workers or the private containerd, which run in their own process groups, so the
// process must stop them before it exits. Under nohup a hangup stays ignored. SIGQUIT keeps Go's dump-and-exit.
func Signals() []os.Signal {
	if signal.Ignored(syscall.SIGHUP) {
		return []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
