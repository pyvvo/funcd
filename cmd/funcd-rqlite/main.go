// Command funcd-rqlite is the entrypoint of funcd's rqlite engine image (ADR-0232 Decision 4): it writes the auth file
// into the container's own filesystem from FUNCD_SQL_PASSWORD, then execs rqlited with the engine's fixed addresses.
package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/pyvvo/funcd/internal/services/sql/engineargs"
)

const (
	rqlited  = "/usr/local/bin/rqlited"
	authFile = "/tmp/auth.json"
	dataDir  = "/data/db"
)

func main() {
	if err := run(os.Getenv, os.WriteFile, syscall.Exec); err != nil {
		fmt.Fprintln(os.Stderr, "funcd-rqlite:", err)
		os.Exit(1)
	}
}

// run is main with its effects injected, for the test.
func run(getenv func(string) string, write func(string, []byte, os.FileMode) error, exec func(string, []string, []string) error) error {
	nodeID := getenv("FUNCD_SQL_NODE_ID")
	if nodeID == "" {
		return fmt.Errorf("FUNCD_SQL_NODE_ID is not set")
	}
	auth, err := engineargs.AuthFile(getenv("FUNCD_SQL_PASSWORD"))
	if err != nil {
		return err
	}
	if err := write(authFile, auth, 0o600); err != nil {
		return fmt.Errorf("write the auth file: %w", err)
	}
	args := engineargs.Args{
		NodeID: nodeID, HTTPAddr: "0.0.0.0:4001", HTTPAdvAddr: "127.0.0.1:4001", RaftAddr: "127.0.0.1:4002",
		AuthFile: authFile, DataDir: dataDir,
	}
	return exec(rqlited, append([]string{rqlited}, args.Argv()...), []string{})
}
