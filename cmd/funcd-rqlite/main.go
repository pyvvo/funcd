// Command funcd-rqlite is the entrypoint of funcd's rqlite engine image (ADR-0232 Decision 4): it writes the auth file
// into the container's own filesystem from FUNCD_SQL_PASSWORD and, with FUNCD_SQL_CDC_ENDPOINT, the change feed's
// config under the directory's epoch (ADR-0235 Decision 1), then execs rqlited with the engine's fixed addresses.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"github.com/pyvvo/funcd/internal/services/sql/engineargs"
)

const (
	rqlited   = "/usr/local/bin/rqlited"
	authFile  = "/tmp/auth.json"
	cdcFile   = "/tmp/cdc.json"
	epochFile = "/data/epoch"
	dataDir   = "/data/db"
)

func main() {
	if err := run(os.Getenv, os.ReadFile, os.WriteFile, syscall.Exec); err != nil {
		fmt.Fprintln(os.Stderr, "funcd-rqlite:", err)
		os.Exit(1)
	}
}

// run is main with its effects injected, for the test.
func run(getenv func(string) string, read func(string) ([]byte, error), write func(string, []byte, os.FileMode) error,
	exec func(string, []string, []string) error) error {
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
	if endpoint := getenv("FUNCD_SQL_CDC_ENDPOINT"); endpoint != "" {
		epoch, err := epochOf(read, write)
		if err != nil {
			return err
		}
		cfg, err := engineargs.CDCConfig(endpoint, epoch)
		if err != nil {
			return err
		}
		if err := write(cdcFile, cfg, 0o600); err != nil {
			return fmt.Errorf("write the change feed config: %w", err)
		}
		args.CDCConfig = cdcFile
	}
	return exec(rqlited, append([]string{rqlited}, args.Argv()...), []string{})
}

// epochOf reads the directory's epoch, writing a new one the first time: it lives as long as the data does.
func epochOf(read func(string) ([]byte, error), write func(string, []byte, os.FileMode) error) (string, error) {
	b, err := read(epochFile)
	switch {
	case err == nil && strings.TrimSpace(string(b)) != "":
		return strings.TrimSpace(string(b)), nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("read the epoch: %w", err)
	}
	epoch, err := engineargs.NewEpoch()
	if err != nil {
		return "", err
	}
	if err := write(epochFile, []byte(epoch+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write the epoch: %w", err)
	}
	return epoch, nil
}
