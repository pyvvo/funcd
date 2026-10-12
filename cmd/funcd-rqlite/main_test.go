package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunWritesAuthAndExecs(t *testing.T) {
	env := map[string]string{"FUNCD_SQL_NODE_ID": "todo-db", "FUNCD_SQL_PASSWORD": "s3cret"}
	var wrote string
	var mode os.FileMode
	var argv, envv []string
	err := run(func(k string) string { return env[k] },
		func(path string, _ []byte, m os.FileMode) error { wrote, mode = path, m; return nil },
		func(prog string, a, e []string) error { argv, envv = append([]string{prog}, a...), e; return nil })
	require.NoError(t, err)
	require.Equal(t, "/tmp/auth.json", wrote, "the auth file stays off the durable directory")
	require.Equal(t, os.FileMode(0o600), mode)
	require.Equal(t, "/usr/local/bin/rqlited", argv[0])
	require.Contains(t, argv, "-http-adv-addr")
	require.Contains(t, argv, "127.0.0.1:4002")
	require.Equal(t, "/data/db", argv[len(argv)-1])
	require.Empty(t, envv, "rqlited does not inherit the password")
}

func TestRunRefusesMissingEnv(t *testing.T) {
	noop := func(string, []byte, os.FileMode) error { return nil }
	exec := func(string, []string, []string) error { return nil }
	require.Error(t, run(func(string) string { return "" }, noop, exec))
	require.Error(t, run(func(k string) string {
		if k == "FUNCD_SQL_NODE_ID" {
			return "db"
		}
		return ""
	}, noop, exec))
}
