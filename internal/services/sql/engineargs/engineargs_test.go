package engineargs_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/services/sql/engineargs"
)

func TestArgv(t *testing.T) {
	a := engineargs.Args{NodeID: "todo-db", HTTPAddr: "0.0.0.0:4001", HTTPAdvAddr: "127.0.0.1:4001",
		RaftAddr: "127.0.0.1:4002", AuthFile: "/tmp/auth.json", DataDir: "/data/db"}
	require.Equal(t, []string{
		"-node-id", "todo-db", "-http-addr", "0.0.0.0:4001", "-http-adv-addr", "127.0.0.1:4001",
		"-raft-addr", "127.0.0.1:4002", "-auth", "/tmp/auth.json", "/data/db",
	}, a.Argv())
}

func TestAuthFile(t *testing.T) {
	raw, err := engineargs.AuthFile("s3cret")
	require.NoError(t, err)
	type user struct {
		Username string   `json:"username"`
		Password string   `json:"password"`
		Perms    []string `json:"perms"`
	}
	var got []user
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, []user{
		{Username: "funcd", Password: "s3cret", Perms: []string{"all"}},
		{Username: "*", Perms: []string{"ready"}},
	}, got, "the engine user gets every permission, anonymous callers only readiness")

	_, err = engineargs.AuthFile("")
	require.Error(t, err, "an empty password is refused")
}
