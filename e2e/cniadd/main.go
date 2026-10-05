// Command cniadd attaches a network namespace to funcd's CNI networks under a chosen attachment ID, with the go-cni
// setup the containerd driver uses. cnitool derives the ID from the netns path, so the duckdb lane uses this to plant
// an attachment under the CNI ID form used before ADR-0179.
package main

import (
	"context"
	"fmt"
	"os"

	gocni "github.com/containerd/go-cni"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: cniadd <cni-bin-dir> <cni-conf-dir> <attachment-id> <netns-path>")
		os.Exit(2)
	}
	if err := add(os.Args[1], os.Args[2], os.Args[3], os.Args[4]); err != nil {
		fmt.Fprintln(os.Stderr, "cniadd:", err)
		os.Exit(1)
	}
}

func add(binDir, confDir, id, netns string) error {
	cni, err := gocni.New(gocni.WithMinNetworkCount(2), gocni.WithPluginConfDir(confDir), gocni.WithPluginDir([]string{binDir}))
	if err != nil {
		return err
	}
	if err := cni.Load(gocni.WithLoNetwork, gocni.WithDefaultConf); err != nil {
		return err
	}
	_, err = cni.Setup(context.Background(), id, netns)
	return err
}
