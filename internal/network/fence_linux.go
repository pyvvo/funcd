//go:build linux

package network

import (
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

const (
	fenceTableName = "funcd_fence"
	ipv6Sysctls    = "/proc/sys/net/ipv6" // the netns's own, for the thread that reads them
	workerLink     = "eth0"               // the interface CNI gives a worker
)

// FenceNetns writes the funcd_fence table into the network namespace at netnsPath: its output chain drops every
// packet but loopback, established or related traffic, and new TCP to an allow endpoint (ADR-0269). A process in that
// netns without CAP_NET_ADMIN cannot change it, and it goes with the netns.
func FenceNetns(netnsPath string, allow []netip.AddrPort) error {
	const op = "network.FenceNetns"
	for _, ap := range allow {
		if !ap.Addr().Is4() {
			return fault.Invalidf(op, "fence endpoint %s is not IPv4", ap)
		}
	}
	f, err := os.Open(netnsPath)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open netns %s", netnsPath)
	}
	defer func() { _ = f.Close() }()
	c, err := nftables.New(nftables.WithNetNSFd(int(f.Fd())))
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open nftables in netns %s", netnsPath)
	}
	t := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: fenceTableName})
	drop := nftables.ChainPolicyDrop
	ch := c.AddChain(&nftables.Chain{
		Name: "output", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityFilter, Policy: &drop,
	})
	for _, exprs := range fenceRules(allow) {
		c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: exprs})
	}
	if err := c.Flush(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "program %s in netns %s", fenceTableName, netnsPath)
	}
	return nil
}

// fenceRules are the output chain's accepts, in order: loopback, established or related, then IPv4 TCP to each allow
// endpoint.
func fenceRules(allow []netip.AddrPort) [][]expr.Any {
	rules := [][]expr.Any{
		concat(matchLoopback(expr.MetaKeyOIF, expr.CmpOpEq), verdict(expr.VerdictAccept)),
		concat(matchEstablished(), verdict(expr.VerdictAccept)),
	}
	for _, ap := range allow {
		rules = append(rules, concat(
			matchIPv4(), matchDstAddr(ap.Addr().As4()), matchL4Proto(unix.IPPROTO_TCP), matchDport(ap.Port()), verdict(expr.VerdictAccept),
		))
	}
	return rules
}

// DisableWorkerIPv6 takes IPv6 from a worker's eth0 in the network namespace at netnsPath, and from any interface made
// there later. Loopback keeps it, as an application may bind :: or ::1 (ADR-0318). A worker cannot change it, as its
// /proc/sys is read-only.
func DisableWorkerIPv6(netnsPath string) error {
	const op = "network.DisableWorkerIPv6"
	f, err := os.Open(netnsPath)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open netns %s", netnsPath)
	}
	defer func() { _ = f.Close() }()
	done := make(chan error, 1)
	go func() {
		// Never unlocked: the thread ends with this goroutine, so no other goroutine runs in the worker's netns.
		runtime.LockOSThread()
		if err := unix.Setns(int(f.Fd()), unix.CLONE_NEWNET); err != nil {
			done <- err
			return
		}
		done <- disableIPv6(ipv6Sysctls)
	}()
	if err := <-done; err != nil {
		return fault.Wrapf(err, fault.Internal, op, "disable IPv6 in netns %s", netnsPath)
	}
	return nil
}

// disableIPv6 writes 1 to disable_ipv6 of the worker's link and of default under sysctls. A plain IPv6 socket loads IPv6
// where it is a module; a kernel that still has no such directory has no IPv6, and then nothing to write.
func disableIPv6(sysctls string) error {
	if fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0); err == nil {
		_ = unix.Close(fd)
	}
	if _, err := os.Stat(sysctls); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	for _, link := range []string{workerLink, "default"} {
		f, err := os.OpenFile(filepath.Join(sysctls, "conf", link, "disable_ipv6"), os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_, err = f.WriteString("1")
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
	}
	return nil
}
