//go:build linux

package network

import (
	"net/netip"
	"os"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

const fenceTableName = "funcd_fence"

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
