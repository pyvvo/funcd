package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeFS is the files run reads and writes, by path.
type fakeFS map[string][]byte

func (f fakeFS) read(path string) ([]byte, error) {
	if b, ok := f[path]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}

func (f fakeFS) write(path string, b []byte, _ os.FileMode) error {
	f[path] = b
	return nil
}

func TestRunWritesAuthAndExecs(t *testing.T) {
	env := map[string]string{"FUNCD_SQL_NODE_ID": "todo-db", "FUNCD_SQL_PASSWORD": "s3cret"}
	var mode os.FileMode
	var argv, envv []string
	files := fakeFS{}
	err := run(func(k string) string { return env[k] }, files.read,
		func(path string, b []byte, m os.FileMode) error { mode = m; return files.write(path, b, m) },
		func(prog string, a, e []string) error { argv, envv = append([]string{prog}, a...), e; return nil })
	require.NoError(t, err)
	require.Contains(t, files, "/tmp/auth.json", "the auth file stays off the durable directory")
	require.Len(t, files, 1, "no change feed without an endpoint")
	require.Equal(t, os.FileMode(0o600), mode)
	require.Equal(t, "/usr/local/bin/rqlited", argv[0])
	require.Contains(t, argv, "-http-adv-addr")
	require.Contains(t, argv, "127.0.0.1:4002")
	require.NotContains(t, argv, "-cdc-config")
	require.Equal(t, "/data/db", argv[len(argv)-1])
	require.Empty(t, envv, "rqlited does not inherit the password")
}

// ADR-0235 Decision 1: with an endpoint, the engine posts every change there under its directory's epoch, kept
// across starts in /data/epoch, and broadcasts its high watermark as the funcd user.
func TestRunWritesTheChangeFeed(t *testing.T) {
	env := map[string]string{"FUNCD_SQL_NODE_ID": "todo-db", "FUNCD_SQL_PASSWORD": "s3cret",
		"FUNCD_SQL_CDC_ENDPOINT": "http://10.63.0.1:7075/v1/changes/tok"}
	files := fakeFS{}
	var argv []string
	exec := func(_ string, a, _ []string) error { argv = a; return nil }
	require.NoError(t, run(func(k string) string { return env[k] }, files.read, files.write, exec))
	epoch := strings.TrimSpace(string(files["/data/epoch"]))
	require.Len(t, epoch, 32, "a 128-bit epoch in hex")
	var cfg map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(files["/tmp/cdc.json"], &cfg))
	require.JSONEq(t, `"http://10.63.0.1:7075/v1/changes/tok"`, string(cfg["endpoint"]))
	require.JSONEq(t, `"`+epoch+`"`, string(cfg["service_id"]))
	require.JSONEq(t, `100`, string(cfg["max_batch_size"]))
	require.JSONEq(t, `10000000000`, string(cfg["transmit_timeout"]), "rqlite reads durations as nanoseconds")
	require.Contains(t, strings.Join(argv, " "), "-cdc-config /tmp/cdc.json -cdc-as funcd /data/db")

	require.NoError(t, run(func(k string) string { return env[k] }, files.read, files.write, exec))
	require.Equal(t, epoch, strings.TrimSpace(string(files["/data/epoch"])), "the epoch lives as long as the directory")
}

func TestRunRefusesMissingEnv(t *testing.T) {
	files := fakeFS{}
	exec := func(string, []string, []string) error { return nil }
	require.Error(t, run(func(string) string { return "" }, files.read, files.write, exec))
	require.Error(t, run(func(k string) string {
		if k == "FUNCD_SQL_NODE_ID" {
			return "db"
		}
		return ""
	}, files.read, files.write, exec))
}
