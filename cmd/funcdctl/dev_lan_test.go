//go:build dev

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The ADR-0361 scenarios. Those that dial the gateway from the LAN use a LAN address of this host and skip on a host
// with none; the client is this host, as any device on its network would be, since the gateway answers on that
// address only when it listens there.

// lanRelayWithin bounds the wait for one message relayed between two WebSockets.
const lanRelayWithin = 10 * time.Second

// lanAddrOrSkip is the first LAN address of this host, or a skip when it has none.
func lanAddrOrSkip(t *testing.T) netip.Addr {
	t.Helper()
	addrs, err := lanAddrs()
	require.NoError(t, err)
	if len(addrs) == 0 {
		t.Skip("this host has no LAN address: no up interface holds an IPv4 address off loopback")
	}
	return addrs[0]
}

// startDevWith boots funcdctl dev over dir with cfg, torn down at test end.
func startDevWith(t *testing.T, dir string, cfg devConfig) *devInstance {
	t.Helper()
	cfg.cacheDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, cfg)
	t.Cleanup(func() {
		cancel()
		if inst != nil {
			_ = inst.stop()
		}
	})
	require.NoError(t, err)
	return inst
}

// portOf is the port of a base URL.
func portOf(t *testing.T, base string) string {
	t.Helper()
	u, err := neturl.Parse(base)
	require.NoError(t, err)
	return u.Port()
}

// requireRefused checks that a TCP connection to addr is refused: nothing listens there.
func requireRefused(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
	}
	require.ErrorIs(t, err, syscall.ECONNREFUSED, "%s takes connections", addr)
}

// banner is the banner dev prints for inst.
func banner(t *testing.T, inst *devInstance) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, (&cli{out: &out}).printBanner(inst))
	return out.String()
}

// wsOpen opens a WebSocket to url, closed when the test ends.
func wsOpen(t *testing.T, url, origin string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig(url, origin)
	require.NoError(t, err)
	ws, err := websocket.DialConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// scenario: dev-lan-off — without --lan or dev.lan the gateway refuses a connection on a LAN address, answers on
// 127.0.0.1, and the banner prints no network line and no LAN warning.
func TestScenarioDevLANOff(t *testing.T) {
	requireNode(t)
	t.Parallel()
	lan := lanAddrOrSkip(t)
	inst := startDevWith(t, devProject(t, nodeFunction("api", "", okBuild)), devConfig{})
	eventuallyServes(t, inst.gatewayURL+"/function/api", `{"data":{}}`, http.StatusOK, `"ok":true`)
	requireRefused(t, net.JoinHostPort(lan.String(), portOf(t, inst.gatewayURL)))
	require.Empty(t, inst.networkURLs)
	got := banner(t, inst)
	require.Contains(t, got, "serving on localhost · Ctrl-C to stop")
	require.NotContains(t, got, "network ")
	require.NotContains(t, got, devLANWarning)
}

// scenario: dev-lan-every-address — with --lan the http-api example answers on a LAN address as on 127.0.0.1, and two
// WebSockets opened at its relay there meet.
func TestScenarioDevLANEveryAddress(t *testing.T) {
	requireNode(t)
	t.Parallel()
	lan := lanAddrOrSkip(t)
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS(filepath.Join(langmod.Dir(t, langmod.TypeScript), "examples", "http-api"))))
	inst := startDevWith(t, dir, devConfig{lan: devLANAll})
	hostPort := net.JoinHostPort(lan.String(), portOf(t, inst.gatewayURL))
	require.Contains(t, inst.networkURLs, "http://"+hostPort)
	waitFunction(t, inst, "devices-api")

	local, _ := get(t, inst.gatewayURL+"/function/devices-api/devices", "")
	require.Equal(t, http.StatusOK, local)
	onLAN, _ := get(t, "http://"+hostPort+"/function/devices-api/devices", "")
	require.Equal(t, http.StatusOK, onLAN)

	relay := "ws://" + hostPort + "/function/devices-api/relay"
	a := wsOpen(t, relay, "http://"+hostPort)
	b := wsOpen(t, relay, "http://"+hostPort)
	require.NoError(t, websocket.Message.Send(a, "left"))
	require.NoError(t, b.SetReadDeadline(time.Now().Add(lanRelayWithin)))
	var msg string
	require.NoError(t, websocket.Message.Receive(b, &msg))
	require.Equal(t, "left", msg)
}

// scenario: dev-lan-loopback-kept — with --lan the control plane and the S3 frontend refuse a connection on a LAN
// address, and both answer on 127.0.0.1.
func TestScenarioDevLANLoopbackKept(t *testing.T) {
	requireNode(t)
	t.Parallel()
	lan := lanAddrOrSkip(t)
	inst := startDevWith(t, devProject(t, nodeFunction("api", "", okBuild)), devConfig{lan: devLANAll})
	for _, base := range []string{inst.controlURL, inst.s3Endpoint} {
		requireRefused(t, net.JoinHostPort(lan.String(), portOf(t, base)))
		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(base, "http://"), time.Second)
		require.NoError(t, err, "%s answers on loopback", base)
		_ = conn.Close()
	}
	_, err := inst.client.List(context.Background(), v1.KindFunction, devNamespace)
	require.NoError(t, err)
}

// scenario: dev-lan-one-address — with --lan=<a LAN address> the gateway answers on that address, refuses 127.0.0.1,
// and the banner's title and gateway lines name the address, with the LAN warning.
func TestScenarioDevLANOneAddress(t *testing.T) {
	requireNode(t)
	t.Parallel()
	lan := lanAddrOrSkip(t)
	inst := startDevWith(t, devProject(t, nodeFunction("api", "", okBuild)), devConfig{lan: lan.String()})
	port := portOf(t, inst.gatewayURL)
	require.Equal(t, "http://"+net.JoinHostPort(lan.String(), port), inst.gatewayURL)
	eventuallyServes(t, inst.gatewayURL+"/function/api", `{"data":{}}`, http.StatusOK, `"ok":true`)
	requireRefused(t, net.JoinHostPort(devLoopback, port))
	require.Empty(t, inst.networkURLs, "the gateway line itself names the address")
	got := banner(t, inst)
	for _, want := range []string{
		"serving on localhost and " + lan.String() + " · Ctrl-C to stop",
		"gateway   " + inst.gatewayURL,
		"POST  " + inst.gatewayURL + "/function/api",
		devLANWarning,
	} {
		require.Contains(t, got, want)
	}
}

// scenario: dev-lan-banner — with dev.lan: true in funcdctl.config.yaml and two LAN addresses, the banner puts one
// network line per address beneath the gateway's line, a Route's line that names no host and a Site's line, none
// beneath a Route's line with a host, keeps the control and s3 lines on 127.0.0.1, and prints the LAN warning first.
func TestScenarioDevLANBanner(t *testing.T) {
	t.Parallel()
	start := devProject(t, map[string]string{devConfigFile: "dev:\n  lan: true\n"})
	var cfg devConfig
	_, _, err := resolveDevRun("test", start, nil, nil, &cfg)
	require.NoError(t, err)
	require.Equal(t, devLANAll, cfg.lan)
	require.Equal(t, ":0", gatewayListen(cfg.lan, cfg.gport))

	obj, ok := v1.NewObject(v1.KindApp)
	require.True(t, ok)
	app := obj.(*v1.App)
	app.Name, app.Namespace = "remote", devNamespace
	app.Spec.Routes = []v1.AppRoute{
		{Name: "api", RouteSpec: v1.RouteSpec{Rules: []v1.RouteRule{{Path: "/api"}}}},
		{Name: "login", RouteSpec: v1.RouteSpec{Host: "login.localhost", Rules: []v1.RouteRule{{Path: "/auth"}}}},
	}
	app.Spec.Sites = []v1.AppSite{{Name: "web"}}
	// Nothing answers this control URL, so the APP line reads "no revision yet".
	client, err := sdk.New("http://127.0.0.1:9")
	require.NoError(t, err)
	inst := &devInstance{app: app, client: client, controlURL: "http://127.0.0.1:61234", s3Endpoint: "http://127.0.0.1:61235",
		s3Region: "us-east-1"}
	lanA, lanB := netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("198.51.100.7")
	inst.setGatewayURLs("[::]:8795", func() ([]netip.Addr, error) { return []netip.Addr{lanA, lanB}, nil })

	got := banner(t, inst)
	order := []string{
		"serving on localhost and the LAN · Ctrl-C to stop",
		"gateway   http://127.0.0.1:8795",
		"network   http://192.0.2.10:8795",
		"network   http://198.51.100.7:8795",
		"control   http://127.0.0.1:61234",
		"s3        http://127.0.0.1:61235",
		"route     http://127.0.0.1:8795/api",
		"network   http://192.0.2.10:8795/api",
		"network   http://198.51.100.7:8795/api",
		"route     http://login.localhost:8795/auth",
		"site      http://127.0.0.1:8795/site/web",
		"network   http://192.0.2.10:8795/site/web",
		"network   http://198.51.100.7:8795/site/web",
		devLANWarning,
		"egress is NOT isolated",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(got[at:], want)
		require.GreaterOrEqual(t, i, 0, "%q after %q in:\n%s", want, got[:at], got)
		at += i + len(want)
	}
	require.Equal(t, 6, strings.Count(got, "network   http://"), "no network line beneath the Route with a host")
}

// scenario: dev-lan-refused — a value that is not true, false or an IP address stops dev, from the flag with no
// funcdctl.config.yaml and from the file's key, and a bare --lan before an address names the = form, a directory after
// it or not.
func TestScenarioDevLANRefused(t *testing.T) {
	t.Parallel()
	_, err := os.Stat(devConfigFile)
	require.True(t, errors.Is(err, os.ErrNotExist), "the flag cases run with no %s", devConfigFile)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"a host name":             {args: []string{"dev", "--lan=tv.local", t.TempDir()}, want: `--lan "tv.local" is not true, false or an IP address`},
		"a space":                 {args: []string{"dev", "--lan", "192.168.1.20"}, want: "write --lan=192.168.1.20"},
		"a space and a directory": {args: []string{"dev", "--lan", "192.168.1.20", "./app"}, want: "write --lan=192.168.1.20"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.ErrorContains(t, execCLI(io.Discard, nil, tc.args...), tc.want)
		})
	}
	t.Run("the key", func(t *testing.T) {
		t.Parallel()
		start := devProject(t, map[string]string{devConfigFile: "dev:\n  lan: 3\n"})
		var cfg devConfig
		_, _, err := resolveDevRun("test", start, nil, nil, &cfg)
		require.ErrorContains(t, err, devConfigFile+`: dev.lan "3" is not true, false or an IP address`)
	})
}

// checkLAN takes "", false, true and any IP address, and refuses anything else, naming it.
func TestCheckLAN(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"", "false", "true", "192.168.1.20", "0.0.0.0", "::", "::1", "2001:db8::5", "fe80::1%en0"} {
		require.NoError(t, checkLAN("test", "--lan", ok), ok)
	}
	for _, bad := range []string{"tv.local", "yes", "3", "192.168.1", "192.168.1.20:8795", "[::1]"} {
		require.ErrorContains(t, checkLAN("test", "--lan", bad), `--lan "`+bad+`" is not true, false or an IP address`)
	}
}

// gatewayListen binds 127.0.0.1 without the opt-in, every address with true, and one address in net.JoinHostPort's
// form, an IPv6 one in brackets.
func TestGatewayListen(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lan   string
		gport int
		want  string
	}{
		{"", 0, "127.0.0.1:0"},
		{"false", 8795, "127.0.0.1:8795"},
		{devLANAll, 8795, ":8795"},
		{devLANAll, 0, ":0"},
		{"192.168.1.20", 8795, "192.168.1.20:8795"},
		{"::1", 8795, "[::1]:8795"},
		{"2001:db8::5", 0, "[2001:db8::5]:0"},
	} {
		require.Equal(t, tc.want, gatewayListen(tc.lan, tc.gport), "%q %d", tc.lan, tc.gport)
	}
}

// gatewayURLs reads the bound address: every address lists the LAN addresses beneath 127.0.0.1, one address is the
// gateway's own URL, and a loopback address is not off loopback.
func TestGatewayURLs(t *testing.T) {
	t.Parallel()
	two := func() ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("198.51.100.7")}, nil
	}
	unused := func() ([]netip.Addr, error) {
		t.Error("the LAN addresses are listed only on every address")
		return nil, nil
	}
	for _, tc := range []struct {
		bound   string
		find    func() ([]netip.Addr, error)
		local   string
		network []string
		off     bool
	}{
		{"[::]:8795", two, "http://127.0.0.1:8795", []string{"http://192.0.2.10:8795", "http://198.51.100.7:8795"}, true},
		{"0.0.0.0:8795", two, "http://127.0.0.1:8795", []string{"http://192.0.2.10:8795", "http://198.51.100.7:8795"}, true},
		{"192.0.2.10:8795", unused, "http://192.0.2.10:8795", nil, true},
		{"[2001:db8::5]:8795", unused, "http://[2001:db8::5]:8795", nil, true},
		{"127.0.0.1:8795", unused, "http://127.0.0.1:8795", nil, false},
		{"[::1]:8795", unused, "http://[::1]:8795", nil, false},
	} {
		local, network, off, err := gatewayURLs(netip.MustParseAddrPort(tc.bound), tc.find)
		require.NoError(t, err, tc.bound)
		require.Equal(t, tc.local, local, tc.bound)
		require.Equal(t, tc.network, network, tc.bound)
		require.Equal(t, tc.off, off, tc.bound)
	}
	failing := errors.New("no interfaces")
	local, network, off, err := gatewayURLs(netip.MustParseAddrPort("[::]:8795"),
		func() ([]netip.Addr, error) { return nil, failing })
	require.ErrorIs(t, err, failing)
	require.Equal(t, "http://127.0.0.1:8795", local)
	require.Empty(t, network)
	require.True(t, off)
}

// lanAddrs lists IPv4 addresses only, none of them loopback.
func TestLANAddrs(t *testing.T) {
	t.Parallel()
	addrs, err := lanAddrs()
	require.NoError(t, err)
	for _, a := range addrs {
		require.True(t, a.Is4() && !a.IsLoopback(), "%s", a)
	}
}

// dev.lan decodes true, false and an IP address as --lan's text, and decodeDevFile refuses any other value.
func TestDecodeDevLAN(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]string{"true": devLANAll, "false": "", "192.168.1.20": "192.168.1.20", `"::1"`: "::1",
		`"false"`: ""} {
		keys, err := decodeDevFile("test", []byte("dev:\n  lan: "+value+"\n"))
		require.NoError(t, err, value)
		require.Equal(t, want, string(keys.LAN), value)
	}
	for value, want := range map[string]string{
		"tv.local":       `dev.lan "tv.local" is not true, false or an IP address`,
		"3":              `dev.lan "3" is not true, false or an IP address`,
		"yes":            `dev.lan "yes" is not true, false or an IP address`,
		"\n    on: true": "lan is not true, false or an IP address",
	} {
		_, err := decodeDevFile("test", []byte("dev:\n  lan: "+value+"\n"))
		require.ErrorContains(t, err, want, value)
	}
}

// The flag replaces dev.lan, false included, and the key alone sets it.
func TestDevLANFlagWinsOverTheKey(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"the key":               {want: devLANAll},
		"a flag wins":           {args: []string{"--lan=false"}, want: ""},
		"an address flag wins":  {args: []string{"--lan=192.168.1.20"}, want: "192.168.1.20"},
		"a bare flag means all": {args: []string{"--lan"}, want: devLANAll},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := devProject(t, map[string]string{devConfigFile: "dev:\n  lan: true\n"})
			cmd := (&cli{out: io.Discard}).devCmd()
			require.NoError(t, cmd.Flags().Parse(tc.args))
			cfg := devConfig{}
			cfg.lan, _ = cmd.Flags().GetString("lan")
			_, _, err := resolveDevRun("test", start, cmd.Flags().Args(), cmd.Flags(), &cfg)
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.lan)
		})
	}
}

// A changed dev.lan takes a restart, as every key outside build, values, apply and env does.
func TestDevLANChangeNeedsARestart(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"lan"}, changedDevKeys(devFileKeys{}, devFileKeys{LAN: devLANAll}))
	require.NotContains(t, devReloadKeys(), "lan")
	require.Contains(t, devFileKeyNames(), "lan")
}

// lanArgs leaves a directory whose name reads as an IP address to dev, and keeps cobra's limit of one argument.
func TestLANArgsLeavesADirectoryNamedLikeAnAddress(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "10.9.8.7"), 0o755))
	t.Chdir(dir)
	cmd := (&cli{out: io.Discard}).devCmd()
	require.NoError(t, cmd.Flags().Parse([]string{"--lan", "10.9.8.7"}))
	require.NoError(t, lanArgs(cmd, cmd.Flags().Args()))
	require.ErrorContains(t, lanArgs(cmd, []string{"10.9.8.7", "other"}), "accepts at most 1 arg")
}
