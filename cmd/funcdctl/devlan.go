//go:build dev

package main

import (
	"errors"
	"net"
	"net/netip"
	neturl "net/url"
	"strconv"

	"github.com/spf13/cobra"
	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/pyvvo/funcd/api/fault"
	platformconfig "github.com/pyvvo/funcd/internal/platform/config"
)

// devLANAll is --lan's value when given alone, and dev.lan: true: the gateway listens on every address (ADR-0361).
const devLANAll = "true"

// devLANOff is --lan=false and dev.lan: false, read as "", the default.
const devLANOff = "false"

// devLoopback is the address every dev listener binds, and the gateway's without --lan.
const devLoopback = "127.0.0.1"

// devLANWarning is the banner's line while the gateway listens off loopback (ADR-0361 Decision 3).
const devLANWarning = "⚠ the gateway is reachable from the LAN without authentication: anyone on the network can call every " +
	"Function, Route and Site"

// checkLAN refuses a value of --lan or dev.lan (name says which) other than "", "false", devLANAll or an IP address.
func checkLAN(op, name, value string) error {
	switch value {
	case "", devLANOff, devLANAll:
		return nil
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return nil
	}
	return fault.Invalidf(op, "%s %q is not true, false or an IP address", name, value)
}

// lanValue is a checked value as dev keeps it: false is "".
func lanValue(value string) string {
	if value == devLANOff {
		return ""
	}
	return value
}

// gatewayListen is the data plane's listen address for a checked lan value and gport (0 takes a free port), one
// address joined with net.JoinHostPort.
func gatewayListen(lan string, gport int) string {
	port := strconv.Itoa(gport)
	switch lanValue(lan) {
	case "":
		return net.JoinHostPort(devLoopback, port)
	case devLANAll:
		return ":" + port
	}
	return net.JoinHostPort(lan, port)
}

// gatewayURLs are the banner's base URLs for the address the data plane bound: local on 127.0.0.1 when it answers
// there, else on its own address; network, one per address that lan returns, only when bound is unspecified; and
// offLoopback, !platformconfig.IsLoopbackAddr(bound.String()).
func gatewayURLs(bound netip.AddrPort, lan func() ([]netip.Addr, error)) (local string, network []string, offLoopback bool, err error) {
	addr := bound.Addr().Unmap()
	bound = netip.AddrPortFrom(addr, bound.Port())
	offLoopback = !platformconfig.IsLoopbackAddr(bound.String())
	if !addr.IsUnspecified() {
		return "http://" + bound.String(), nil, offLoopback, nil
	}
	port := strconv.Itoa(int(bound.Port()))
	local = "http://" + net.JoinHostPort(devLoopback, port)
	addrs, err := lan()
	for _, a := range addrs {
		network = append(network, "http://"+net.JoinHostPort(a.String(), port))
	}
	return local, network, offLoopback, err
}

// lanAddrs are the IPv4 addresses of the machine's up interfaces, loopback ones left out, in net.Interfaces' order. An
// interface whose addresses cannot be read is left out.
func lanAddrs() ([]netip.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, aerr := ifc.Addrs()
		if aerr != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if ip = ip.Unmap(); ok && ip.Is4() && !ip.IsLoopback() {
				out = append(out, ip)
			}
		}
	}
	return out, nil
}

// setGatewayURLs sets the banner's gateway URLs from the address the data plane bound, as gatewayURLs reads it. When
// the LAN addresses cannot be listed, the gateway answers on them all the same and the banner prints none of them.
func (d *devInstance) setGatewayURLs(bound string, find func() ([]netip.Addr, error)) {
	ap, err := netip.ParseAddrPort(bound)
	if err != nil {
		d.gatewayURL = "http://" + bound
		return
	}
	if find == nil {
		find = lanAddrs
	}
	var lerr error
	d.gatewayURL, d.networkURLs, d.offLoopback, lerr = gatewayURLs(ap, find)
	if lerr != nil && d.logger != nil {
		d.logger.Warn("cannot list the LAN addresses for the banner", "err", lerr)
	}
}

// lanTitle is what the banner's title adds while the gateway listens off loopback: the LAN on every address, else the
// one address it listens on.
func (d *devInstance) lanTitle() string {
	if u, err := neturl.Parse(d.gatewayURL); err == nil && u.Hostname() != devLoopback {
		return u.Hostname()
	}
	return "the LAN"
}

// devLANKey is dev.lan: true, false or an IP address, decoded as --lan's text: devLANAll, "" or the address. A
// scalar of another kind keeps its text, which decodeDevFile then refuses.
type devLANKey string

func (k *devLANKey) UnmarshalYAML(n *yamlv3.Node) error {
	if n.Kind != yamlv3.ScalarNode {
		return errors.New("lan is not true, false or an IP address")
	}
	if n.Tag == "!!bool" {
		var on bool
		if err := n.Decode(&on); err != nil {
			return err
		}
		*k = ""
		if on {
			*k = devLANAll
		}
		return nil
	}
	*k = devLANKey(lanValue(n.Value))
	return nil
}

// lanArgs is dev's cobra Args: with --lan's value devLANAll, a first argument that reads as an IP address and is no
// directory is refused, naming --lan=<it>; then cobra.MaximumNArgs(1).
func lanArgs(cmd *cobra.Command, args []string) error {
	if f := cmd.Flags().Lookup("lan"); f != nil && f.Changed && f.Value.String() == devLANAll && len(args) > 0 {
		if _, err := netip.ParseAddr(args[0]); err == nil && !dirExists(args[0]) {
			return fault.Invalidf("funcdctl dev", "--lan takes its address after =: write --lan=%s", args[0])
		}
	}
	return cobra.MaximumNArgs(1)(cmd, args)
}
