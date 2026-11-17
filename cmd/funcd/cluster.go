package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/stopsignal"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// role is what one funcd process is (FEAT-0002 F241): the all-in-one, the control plane alone, or a worker node.
type role int

const (
	roleAll    role = iota // funcd: the whole platform; with cluster.listenAddr, worker nodes may join it too
	roleServer             // funcd server: the API, the metastore and the node API, with no namespace of its own
	roleNode               // funcd node: a worker node that runs the namespaces placed on it
)

// defaultClusterAddr is the node API's address when funcd server's config sets none; joinTimeout bounds funcd node join.
const (
	defaultClusterAddr = "0.0.0.0:7443"
	joinTimeout        = time.Minute
)

// prepare fills the role's own defaults into cfg.
func (r role) prepare(cfg config.Config) config.Config {
	if r == roleServer && cfg.Cluster.ListenAddr == "" {
		cfg.Cluster.ListenAddr = defaultClusterAddr
	}
	return cfg
}

// servesNodes reports whether the role serves the node API, so its store keeps the change log the nodes follow.
func (r role) servesNodes(cfg config.Config) bool {
	return r == roleServer || (r == roleAll && cfg.Cluster.ListenAddr != "")
}

// option is the role's platform option: the node API of a control plane, the agent of a worker node, or none.
func (r role) option(cfg config.Config) funcd.Option {
	switch {
	case r == roleNode:
		return funcd.WithNodeAgent(funcd.NodeAgentOptions{Dir: cfg.Cluster.DataDir, Version: version.Get().Version})
	case r.servesNodes(cfg):
		return funcd.WithCluster(funcd.ClusterOptions{Dir: cfg.Cluster.DataDir, ListenAddr: cfg.Cluster.ListenAddr,
			Hosts: cfg.Cluster.Hosts, LocalRuns: r == roleAll})
	}
	return nil
}

// newServerCmd is `funcd server`: the control plane alone, which worker nodes join (FEAT-0002 F241).
func newServerCmd(out io.Writer) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the control plane alone: the API, the metastore and the node API that worker nodes join",
		Long: "Run the control plane alone: the API, the metastore and the node API that worker nodes join, on " +
			"cluster.listenAddr (0.0.0.0:7443 when unset). It runs no namespace itself: each runs on the worker node its " +
			"spec.node names.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serveAs(cmd.Context(), configPath, nil, out, roleServer)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "",
		"path to funcdconfig.yaml (else $FUNCD_CONFIG, ./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml)")
	return cmd
}

// newNodeCmd is `funcd node`, a worker node, and `funcd node join`, which joins one to a control plane.
func newNodeCmd(out io.Writer) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Run a worker node: the namespaces placed on it, followed from the control plane it joined",
		Long: "Run a worker node: it follows the namespaces placed on it from the control plane it joined with funcd node " +
			"join, runs their workers, edge and stores, and writes their status back. Its own API answers reads only.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serveAs(cmd.Context(), configPath, nil, out, roleNode)
		},
	}
	cmd.PersistentFlags().StringVar(&configPath, "config", "",
		"path to funcdconfig.yaml (else $FUNCD_CONFIG, ./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml)")
	cmd.AddCommand(newNodeJoinCmd(out, &configPath))
	return cmd
}

func newNodeJoinCmd(out io.Writer, configPath *string) *cobra.Command {
	var server, token string
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join a control plane with a one-time token, and keep this node's identity",
		Long: "Join a control plane with a one-time token from funcdctl node token. funcd first checks that the control " +
			"plane at --server presents the CA the token names, then swaps the token for this node's certificate, kept " +
			"under cluster.dataDir. The token works once.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Locate(*configPath)
			if err != nil {
				return err
			}
			cfg, err := config.Load(path, config.Flags{})
			if err != nil {
				return err
			}
			if token == "" {
				token = os.Getenv("FUNCD_JOIN_TOKEN")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), stopsignal.Signals()...)
			defer stop()
			ctx, cancel := context.WithTimeout(ctx, joinTimeout)
			defer cancel()
			name, err := agent.Join(ctx, server, token, cfg.Cluster.DataDir, version.Get().Version, string(v1.HostPlatform()))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "joined as worker node %s; start it with funcd node\n", name)
			return err
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "the control plane's node API, such as https://homebox.lan:7443")
	cmd.Flags().StringVar(&token, "token", "", "the one-time join token (else $FUNCD_JOIN_TOKEN)")
	_ = cmd.MarkFlagRequired("server")
	return cmd
}
