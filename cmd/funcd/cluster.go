package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/durable"
	"github.com/pyvvo/funcd/internal/platform/stopsignal"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/runtime/process"
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

// nodeSecretsKey is the key file a worker node encrypts its Secrets with when its config names none (FEAT-0002 decision
// 8). Losing it loses nothing: the control plane holds every Secret, and a new node takes them again.
const nodeSecretsKey = "secrets.key"

// secretsKey gives a worker node its own Secrets key under cluster.dataDir when secrets.encryptionKeyFile is unset, so
// the Secrets it pulls stay encrypted at rest.
func (r role) secretsKey(cfg config.Config) (config.Config, error) {
	if r != roleNode || cfg.Secrets.EncryptionKeyFile != "" {
		return cfg, nil
	}
	path := filepath.Join(cfg.Cluster.DataDir, nodeSecretsKey)
	_, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return cfg, fmt.Errorf("generate the node's Secrets key: %w", err)
		}
		if err := (durable.Disk{}).MkdirAll(cfg.Cluster.DataDir, 0o700); err != nil {
			return cfg, fmt.Errorf("create %s: %w", cfg.Cluster.DataDir, err)
		}
		if err := (durable.Disk{}).WriteFile(cfg.Cluster.DataDir, nodeSecretsKey, key, 0o600); err != nil {
			return cfg, fmt.Errorf("write the node's Secrets key: %w", err)
		}
	case err != nil:
		return cfg, fmt.Errorf("read the node's Secrets key: %w", err)
	}
	cfg.Secrets.EncryptionKeyFile = path
	return cfg, nil
}

// runsWorkers reports whether the role runs namespaces: a control plane alone runs none, so it needs no container runtime
// and none of what workers reach.
func (r role) runsWorkers() bool { return r != roleServer }

// executionOptions are the role's runtime and its shims.
func (r role) executionOptions(ctx context.Context, cfg config.Config, log *slog.Logger) ([]funcd.Option, func() error, error) {
	if r.runsWorkers() {
		return executionOptions(ctx, cfg, log)
	}
	rt := process.New(log)
	return []funcd.Option{funcd.WithRuntime(rt)}, rt.Close, nil
}

// hasJoined reports whether this machine joined a control plane as a worker node.
func hasJoined(cfg config.Config) bool {
	_, err := agent.JoinedName(cfg.Cluster.DataDir)
	return err == nil
}

// sealer seals the platform backup; a worker node's names no key (FEAT-0002 F251).
func (r role) sealer(cfg config.Config, master []byte, log *slog.Logger) (*envelope.Sealer, error) {
	if r == roleNode {
		return envelope.FromNodeConfig(cfg, log)
	}
	return envelope.FromConfig(cfg, master, log)
}

// nodeBackups puts a worker node's backups under nodes/<name>/ of backup.target and of kvstore.backup.target (FEAT-0002
// F251): each node backs up its own stores, so the nodes and the control plane share one target without fencing each
// other.
func (r role) nodeBackups(cfg config.Config) (config.Config, error) {
	if r != roleNode || cfg.Backup.Target == "" && cfg.Kvstore.Backup.Target == "" {
		return cfg, nil
	}
	name, err := agent.JoinedName(cfg.Cluster.DataDir)
	if err != nil {
		return cfg, err
	}
	for _, t := range []*string{&cfg.Backup.Target, &cfg.Kvstore.Backup.Target} {
		if *t != "" {
			*t = nodeTarget(*t, name)
		}
	}
	return cfg, nil
}

// makeTargets creates the nodes/<name>/ directory of a worker node's file:// targets, where the target itself exists: one
// that is not there, such as a disk that is not mounted, is left for backup.Open to refuse.
func (r role) makeTargets(cfg config.Config) error {
	if r != roleNode {
		return nil
	}
	for _, t := range []string{cfg.Backup.Target, cfg.Kvstore.Backup.Target} {
		u, err := url.Parse(t)
		if err != nil || u.Scheme != "file" {
			continue
		}
		dir := filepath.FromSlash(u.Path)
		if _, err := os.Stat(filepath.Dir(filepath.Dir(dir))); err != nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create the node's part of the backup target: %w", err)
		}
	}
	return nil
}

// nodeTarget is target's nodes/<node>/ part: the prefix parameter of an s3:// target, a subdirectory of a file:// one.
// Any other target is left for backup.Open to refuse.
func nodeTarget(target string, node v1.ObjectName) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	switch u.Scheme {
	case "s3":
		q := u.Query()
		prefix := q.Get("prefix")
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		q.Set("prefix", prefix+"nodes/"+string(node)+"/")
		u.RawQuery = q.Encode()
	case "file":
		u.Path = path.Join(u.Path, "nodes", string(node))
	default:
		return target
	}
	return u.String()
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
			"cluster.listenAddr (0.0.0.0:7443 when unset). It runs no namespace itself, so it needs no container runtime and " +
			"opens no data plane: each namespace runs on the worker node its spec.node names.",
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
