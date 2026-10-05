package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const (
	adminToken  = "adm-1111111111111111"
	devAToken   = "dev-a-2222222222222222"
	devBToken   = "dev-b-3333333333333333"
	viewerToken = "view-4444444444444444"
	legacyToken = "legacy-5555555555555555"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func debugLogger(buf *syncBuf) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// tokenFile writes content to dir/name at mode 0600, then sets mode.
func tokenFile(t *testing.T, dir, name, content string, mode fs.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	require.NoError(t, os.Chmod(p, mode))
	return p
}

func entry(path, role string, namespaces ...string) string {
	s := "    - tokenFile: " + path + "\n      role: " + role + "\n"
	if len(namespaces) > 0 {
		s += "      namespaces:\n"
		for _, n := range namespaces {
			s += "        - " + n + "\n"
		}
	}
	return s
}

// loadConfig writes body as a funcdconfig.yaml over the file substrate in dataDir and loads it.
func loadConfig(t *testing.T, dataDir, body string) (config.Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: \""+dataDir+"\"\n"+body), 0o600))
	return config.Load(path, config.Flags{})
}

// fourCredentials is an admin, a developer in team-a, a developer in team-b and a viewer in team-a.
func fourCredentials(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg, err := loadConfig(t, shortDataDir(t), "auth:\n  credentials:\n"+
		entry(tokenFile(t, dir, "admin", adminToken+"\n", 0o600), "admin")+
		entry(tokenFile(t, dir, "dev-a", devAToken, 0o600), "developer", "team-a")+
		entry(tokenFile(t, dir, "dev-b", devBToken, 0o400), "developer", "team-b")+
		entry(tokenFile(t, dir, "viewer", viewerToken, 0o600), "viewer", "team-a"))
	require.NoError(t, err)
	return cfg
}

type credPlatform struct {
	p   *funcd.Platform
	log *syncBuf
}

// startCredPlatform runs funcd.New(InMemory(), <credentialOption(cfg)>, WithEdgeAuth(), WithLogger(debug)).
func startCredPlatform(t *testing.T, cfg config.Config) credPlatform {
	t.Helper()
	buf := &syncBuf{}
	log := debugLogger(buf)
	opt, err := credentialOption(cfg, log)
	require.NoError(t, err)
	p, err := funcd.New(funcd.InMemory(), opt, funcd.WithEdgeAuth(), funcd.WithLogger(log))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})
	return credPlatform{p: p, log: buf}
}

func (cp credPlatform) client(t *testing.T, token string) *sdk.Client {
	t.Helper()
	c, err := sdk.New("http://"+cp.p.Addr(), sdk.WithToken(token))
	require.NoError(t, err)
	return c
}

// invoke calls ns/name on the data plane with token as a bearer ("" ⇒ none) and returns the status.
func (cp credPlatform) invoke(t *testing.T, ns, name, token string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+cp.p.DataPlaneAddr()+"/function/"+name, nil)
	require.NoError(t, err)
	req.Header.Set("X-Funcd-Namespace", ns)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func namespaceObj(name string) *v1.Namespace {
	n := &v1.Namespace{TypeMeta: v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}}
	n.Name = v1.ObjectName(name)
	return n
}

func functionObj(ns, name string) *v1.Function {
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), v1.NamespaceName(ns), "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "registry.example/fn:v1"
	return fn
}

func secretObj(ns, name string) *v1.Secret {
	s := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
	s.Name, s.Namespace, s.ResourceGroup = v1.ObjectName(name), v1.NamespaceName(ns), "rg1"
	s.Spec.Data = map[string][]byte{"API_KEY": []byte("v")}
	return s
}

func requireKind(t *testing.T, want fault.Kind, err error, msg string) {
	t.Helper()
	require.Equal(t, want, fault.KindOf(err), "%s: %v", msg, err)
}

// scenario: admin-applies-namespace-defaults
func TestScenarioAdminAppliesNamespaceDefaults(t *testing.T) {
	ctx := context.Background()
	admin := startCredPlatform(t, fourCredentials(t)).client(t, adminToken)

	n := namespaceObj("public")
	n.Spec.DefaultExposure = v1.ExposureExplicit
	n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	_, err := admin.Apply(ctx, n)
	require.NoError(t, err)

	got, err := admin.Get(ctx, v1.KindNamespace, "", "public")
	require.NoError(t, err)
	ns := got.(*v1.Namespace)
	require.Equal(t, v1.ExposureExplicit, ns.Spec.DefaultExposure)
	require.NotNil(t, ns.Spec.EdgeDefaults)
	require.NotNil(t, ns.Spec.EdgeDefaults.Auth)
	require.Equal(t, v1.AuthAuthenticated, ns.Spec.EdgeDefaults.Auth.Mode)

	for _, k := range []v1.Kind{v1.KindNamespace, v1.KindRuntimeClass, v1.KindGateway, v1.KindWorkerNode} {
		_, err := admin.List(ctx, k, "")
		require.NoError(t, err, "admin lists %s", k)
	}
}

// scenario: developer-scoped-to-its-namespaces
func TestScenarioDeveloperScopedToItsNamespaces(t *testing.T) {
	ctx := context.Background()
	cp := startCredPlatform(t, fourCredentials(t))
	admin, dev := cp.client(t, adminToken), cp.client(t, devAToken)
	for _, n := range []string{"team-a", "team-b"} {
		_, err := admin.Apply(ctx, namespaceObj(n))
		require.NoError(t, err)
	}

	_, err := dev.Apply(ctx, functionObj("team-a", "api"))
	require.NoError(t, err, "a developer applies in its namespace")

	_, err = dev.Apply(ctx, functionObj("team-b", "api"))
	requireKind(t, fault.Forbidden, err, "apply in team-b")
	_, err = dev.Apply(ctx, namespaceObj("team-c"))
	requireKind(t, fault.Forbidden, err, "apply a Namespace")
	_, err = dev.Get(ctx, v1.KindNamespace, "", "team-a")
	requireKind(t, fault.Forbidden, err, "get a Namespace")
	_, err = dev.List(ctx, v1.KindNamespace, "")
	requireKind(t, fault.Forbidden, err, "list Namespaces")
	err = dev.Delete(ctx, v1.KindNamespace, "", "team-a")
	requireKind(t, fault.Forbidden, err, "delete a Namespace")
	_, err = dev.List(ctx, v1.KindRuntimeClass, "")
	requireKind(t, fault.Forbidden, err, "list RuntimeClasses")
}

// scenario: viewer-reads-only
func TestScenarioViewerReadsOnly(t *testing.T) {
	ctx := context.Background()
	cp := startCredPlatform(t, fourCredentials(t))
	admin, viewer := cp.client(t, adminToken), cp.client(t, viewerToken)
	_, err := admin.Apply(ctx, namespaceObj("team-a"))
	require.NoError(t, err)
	_, err = cp.client(t, devAToken).Apply(ctx, functionObj("team-a", "api"))
	require.NoError(t, err)

	_, err = viewer.Get(ctx, v1.KindFunction, "team-a", "api")
	require.NoError(t, err, "viewer gets a Function")
	_, err = viewer.List(ctx, v1.KindFunction, "team-a")
	require.NoError(t, err, "viewer lists Functions")

	_, err = viewer.Apply(ctx, functionObj("team-a", "api2"))
	requireKind(t, fault.Forbidden, err, "viewer applies")
	err = viewer.Delete(ctx, v1.KindFunction, "team-a", "api")
	requireKind(t, fault.Forbidden, err, "viewer deletes")
	_, err = viewer.Get(ctx, v1.KindNamespace, "", "team-a")
	requireKind(t, fault.Forbidden, err, "viewer gets a Namespace")
}

// scenario: viewer-cannot-read-secrets
func TestScenarioViewerCannotReadSecrets(t *testing.T) {
	ctx := context.Background()
	cp := startCredPlatform(t, fourCredentials(t))
	_, err := cp.client(t, adminToken).Apply(ctx, namespaceObj("team-a"))
	require.NoError(t, err)
	dev, viewer := cp.client(t, devAToken), cp.client(t, viewerToken)
	_, err = dev.Apply(ctx, secretObj("team-a", "db"))
	require.NoError(t, err)

	_, err = viewer.Get(ctx, v1.KindSecret, "team-a", "db")
	requireKind(t, fault.Forbidden, err, "viewer gets a Secret")
	_, err = viewer.List(ctx, v1.KindSecret, "team-a")
	requireKind(t, fault.Forbidden, err, "viewer lists Secrets")
	_, err = dev.Get(ctx, v1.KindSecret, "team-a", "db")
	require.NoError(t, err, "the developer reads the Secret")
}

// scenario: legacy-token-is-developer
func TestScenarioLegacyTokenIsDeveloper(t *testing.T) {
	ctx := context.Background()
	t.Run("auth.token", func(t *testing.T) {
		cfg, err := loadConfig(t, shortDataDir(t), "auth:\n  token: "+legacyToken+"\n  namespaces:\n    - team-a\n")
		require.NoError(t, err)
		c := startCredPlatform(t, cfg).client(t, legacyToken)
		_, err = c.Apply(ctx, functionObj("team-a", "api"))
		require.NoError(t, err, "the shorthand token is a developer in team-a")
		_, err = c.Apply(ctx, namespaceObj("team-a"))
		requireKind(t, fault.Forbidden, err, "a developer applies no Namespace")
	})
	t.Run("built-in", func(t *testing.T) {
		cfg, err := loadConfig(t, shortDataDir(t), "")
		require.NoError(t, err)
		cp := startCredPlatform(t, cfg)
		require.Contains(t, cp.log.String(), "built-in dev token", "the startup log warns")
		c := cp.client(t, funcd.DevToken)
		_, err = c.Apply(ctx, functionObj("default", "api"))
		require.NoError(t, err, "the dev token is a developer in default")
		_, err = c.Apply(ctx, functionObj("team-a", "api"))
		requireKind(t, fault.Forbidden, err, "outside default")
		_, err = c.Apply(ctx, namespaceObj("team-a"))
		requireKind(t, fault.Forbidden, err, "a developer applies no Namespace")
	})
}

// scenario: edge-accepts-listed-tokens-in-scope
func TestScenarioEdgeAcceptsListedTokensInScope(t *testing.T) {
	cp := startCredPlatform(t, fourCredentials(t))
	n := namespaceObj("team-a")
	n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	_, err := cp.client(t, adminToken).Apply(context.Background(), n)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return cp.invoke(t, "team-a", "api", "") == http.StatusUnauthorized },
		5*time.Second, 20*time.Millisecond, "the namespace stance is authenticated")
	for _, tok := range []string{adminToken, devAToken, viewerToken} {
		require.Equal(t, http.StatusNotFound, cp.invoke(t, "team-a", "api", tok), "the PEP lets a listed in-scope token through")
	}
	require.Equal(t, http.StatusUnauthorized, cp.invoke(t, "team-a", "api", "unlisted-token"))
	require.Equal(t, http.StatusForbidden, cp.invoke(t, "team-a", "api", devBToken), "a developer of team-b")
}

// scenario: no-token-in-logs
func TestScenarioNoTokenInLogs(t *testing.T) {
	ctx := context.Background()
	cp := startCredPlatform(t, fourCredentials(t))
	n := namespaceObj("team-a")
	n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	var errs []error
	_, err := cp.client(t, adminToken).Apply(ctx, n)
	require.NoError(t, err)
	tokens := []string{adminToken, devAToken, devBToken, viewerToken, legacyToken}
	for _, tok := range tokens {
		c := cp.client(t, tok)
		_, err := c.List(ctx, v1.KindFunction, "team-a")
		errs = append(errs, err)
		_, err = c.Apply(ctx, functionObj("team-a", "api"))
		errs = append(errs, err)
		_, err = c.Get(ctx, v1.KindSecret, "team-a", "db")
		errs = append(errs, err)
		_, err = c.List(ctx, v1.KindNamespace, "")
		errs = append(errs, err)
		cp.invoke(t, "team-a", "api", tok)
		cp.invoke(t, "team-b", "api", tok)
	}
	require.Contains(t, cp.log.String(), "static credentials loaded")

	for _, bad := range append(badTokenFileCases(t), conflictingCases(t)...) {
		t.Run(bad.name, func(t *testing.T) { errs = append(errs, requireRefused(t, bad, cp.log)) })
	}
	logs := cp.log.String()
	for _, tok := range append(tokens, funcd.DevToken) {
		require.NotContains(t, logs, tok, "a token value reached the log")
		for _, e := range errs {
			if e != nil {
				require.NotContains(t, e.Error(), tok, "a token value reached an error")
			}
		}
	}
}

type startCase struct {
	name, body, key string
	env             [2]string
}

// requireRefused runs config.Load + buildOptions the way main does and asserts a refusal before Badger opens:
// fault.Invalid naming the key, and no store/ directory. Its logs go to log.
func requireRefused(t *testing.T, c startCase, log *syncBuf) error {
	t.Helper()
	if c.env[0] != "" {
		t.Setenv(c.env[0], c.env[1])
	}
	dataDir := shortDataDir(t)
	cfg, err := loadConfig(t, dataDir, c.body)
	if err == nil {
		_, _, _, _, err = buildOptions(context.Background(), cfg, debugLogger(log))
	}
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.Contains(t, err.Error(), c.key)
	_, serr := os.Stat(filepath.Join(dataDir, "store"))
	require.True(t, errors.Is(serr, fs.ErrNotExist), "refused before the metastore opened")
	return err
}

func badTokenFileCases(t *testing.T) []startCase {
	t.Helper()
	dir := t.TempDir()
	good := entry(tokenFile(t, dir, "good", adminToken, 0o600), "admin")
	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	sub := filepath.Join(dir, "subdir")
	require.NoError(t, os.Mkdir(sub, 0o700))
	cases := []startCase{
		{name: "missing", body: entry(filepath.Join(dir, "absent"), "viewer", "team-a")},
		{name: "directory", body: entry(sub, "viewer", "team-a")},
		{name: "fifo", body: entry(fifo, "viewer", "team-a")},
		{name: "empty", body: entry(tokenFile(t, dir, "empty", "", 0o600), "viewer", "team-a")},
		{name: "whitespace", body: entry(tokenFile(t, dir, "blank", " \t\n\n", 0o600), "viewer", "team-a")},
		{name: "two-lines", body: entry(tokenFile(t, dir, "two", viewerToken+"\n"+devBToken+"\n", 0o600), "viewer", "team-a")},
		{name: "over-4KiB", body: entry(tokenFile(t, dir, "big", strings.Repeat("x", maxTokenFileBytes+1), 0o600), "viewer", "team-a")},
	}
	if os.Geteuid() != 0 {
		cases = append(cases, startCase{name: "unreadable", body: entry(tokenFile(t, dir, "locked", viewerToken, 0o000), "viewer", "team-a")})
	}
	for i := range cases {
		cases[i].body = "auth:\n  credentials:\n" + good + cases[i].body
		cases[i].key = "auth.credentials[1].tokenFile"
	}
	return cases
}

// scenario: bad-token-file-refuses-start
func TestScenarioBadTokenFileRefusesStart(t *testing.T) {
	for _, c := range badTokenFileCases(t) {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			_ = requireRefused(t, c, &syncBuf{})
			require.Less(t, time.Since(start), time.Second, "a FIFO never blocks startup")
		})
	}
}

// scenario: exposed-token-file-refuses-start
func TestScenarioExposedTokenFileRefusesStart(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []fs.FileMode{0o640, 0o604, 0o620} {
		t.Run(mode.String(), func(t *testing.T) {
			p := tokenFile(t, dir, "tok-"+mode.String(), adminToken, mode)
			err := requireRefused(t, startCase{body: "auth:\n  credentials:\n" + entry(p, "admin"), key: "auth.credentials[0].tokenFile"}, &syncBuf{})
			require.Contains(t, err.Error(), fmt.Sprintf("%#o", mode.Perm()))
			require.Contains(t, err.Error(), "chmod 0600")
		})
	}
}

func conflictingCases(t *testing.T) []startCase {
	t.Helper()
	dir := t.TempDir()
	admin := tokenFile(t, dir, "admin", adminToken, 0o600)
	dev := tokenFile(t, dir, "dev", devAToken, 0o600)
	same := tokenFile(t, dir, "same", devAToken+"\n", 0o600)
	devTok := tokenFile(t, dir, "devtoken", funcd.DevToken, 0o600)
	list := "auth:\n  credentials:\n" + entry(admin, "admin")
	return []startCase{
		{name: "beside-token", body: list + "  token: " + legacyToken + "\n", key: "auth.token (FUNCD_TOKEN)"},
		{name: "beside-FUNCD_TOKEN", body: list, key: "auth.token (FUNCD_TOKEN)", env: [2]string{"FUNCD_TOKEN", legacyToken}},
		{name: "beside-namespaces", body: list + "  namespaces:\n    - team-a\n", key: "auth.namespaces (FUNCD_AUTH_NAMESPACES)"},
		{name: "empty-list", body: "auth:\n  credentials: []\n", key: "auth.credentials"},
		{name: "commented-out", body: "auth:\n  credentials:\n    # - tokenFile: x\n    #   role: admin\n", key: "auth.credentials"},
		{name: "unknown-role", body: "auth:\n  credentials:\n" + entry(admin, "root"), key: "auth.credentials[0].role"},
		{name: "admin-with-namespaces", body: "auth:\n  credentials:\n" + entry(admin, "admin", "team-a"), key: "auth.credentials[0].namespaces"},
		{name: "empty-namespaces", body: "auth:\n  credentials:\n" + entry(dev, "developer") + "      namespaces: []\n", key: "auth.credentials[0].namespaces"},
		{name: "same-token", body: "auth:\n  credentials:\n" + entry(dev, "developer", "team-a") + entry(same, "viewer", "team-a"), key: `"auth.credentials[0]" and "auth.credentials[1]"`},
		{name: "dev-token", body: "auth:\n  credentials:\n" + entry(devTok, "admin"), key: "auth.credentials[0].tokenFile"},
	}
}

// scenario: conflicting-credentials-refuse-start
func TestScenarioConflictingCredentialsRefuseStart(t *testing.T) {
	for _, c := range conflictingCases(t) {
		t.Run(c.name, func(t *testing.T) {
			_ = requireRefused(t, c, &syncBuf{})
		})
	}
}

type fakeInfo struct {
	mode fs.FileMode
	sys  *syscall.Stat_t
}

func (f fakeInfo) Name() string       { return "tok" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any { //nolint:forbidigo // os.FileInfo's method signature
	if f.sys == nil {
		return nil
	}
	return f.sys
}

// ADR-0171 Decision 2: regular, no group or other bit, owned by the effective UID or root.
func TestCheckTokenFile(t *testing.T) {
	const euid = 1000
	owned := func(uid uint32) *syscall.Stat_t { return &syscall.Stat_t{Uid: uid} }
	for _, tc := range []struct {
		name string
		fi   fakeInfo
		want string
	}{
		{"0600-owner", fakeInfo{0o600, owned(euid)}, ""},
		{"0400-owner", fakeInfo{0o400, owned(euid)}, ""},
		{"0600-root", fakeInfo{0o600, owned(0)}, ""},
		{"0640", fakeInfo{0o640, owned(euid)}, "chmod 0600"},
		{"0604", fakeInfo{0o604, owned(euid)}, "0604"},
		{"0602", fakeInfo{0o602, owned(euid)}, "0602"},
		{"0610", fakeInfo{0o610, owned(euid)}, "0610"},
		{"0601", fakeInfo{0o601, owned(euid)}, "0601"},
		{"other-owner", fakeInfo{0o600, owned(1001)}, "uid 1001"},
		{"no-stat", fakeInfo{0o600, nil}, "no owner information"},
		{"directory", fakeInfo{fs.ModeDir | 0o700, owned(euid)}, "not a regular file"},
		{"fifo", fakeInfo{fs.ModeNamedPipe | 0o600, owned(euid)}, "not a regular file"},
		{"symlink-itself", fakeInfo{fs.ModeSymlink | 0o600, owned(euid)}, "not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkTokenFile(tc.fi, euid)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

// ADR-0171 Decision 2: at most 4 KiB, ASCII whitespace trimmed, one printable token.
func TestReadToken(t *testing.T) {
	_, err := readToken(endless{})
	require.ErrorContains(t, err, "larger than 4096 bytes", "an endless reader is cut at the limit")

	tok, err := readToken(strings.NewReader("abc-123\n"))
	require.NoError(t, err)
	require.Equal(t, "abc-123", tok, "a trailing newline is trimmed")

	tok, err = readToken(strings.NewReader(strings.Repeat("z", maxTokenFileBytes)))
	require.NoError(t, err)
	require.Len(t, tok, maxTokenFileBytes)

	_, err = readToken(strings.NewReader("one\ntwo\n"))
	require.ErrorContains(t, err, "one line")
	require.NotContains(t, err.Error(), "one\n")

	_, err = readToken(strings.NewReader(" \t\r\n "))
	require.ErrorContains(t, err, "no token")

	_, err = readToken(strings.NewReader(""))
	require.ErrorContains(t, err, "no token")
}
