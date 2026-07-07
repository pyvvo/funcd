package s3gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/versity/versitygw/s3api"
	"github.com/versity/versitygw/s3api/middlewares"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	authz "github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/blob"
)

// defaultMaxUpload is the fail-closed buffer cap on a single buffered object when a
// caller's Deps does not set one (ADR-0080): 1 GiB.
const defaultMaxUpload = 1 << 30

// maxInFlightRequests caps concurrent in-flight requests (ADR-0085): versitygw's
// RateLimiter is a weighted semaphore of this size, so it MUST be > 0 or every request
// is rejected 503. A high cap keeps the node-private gateway effectively unthrottled.
const maxInFlightRequests = 4096

// maxMultipartParts is the S3 ceiling on multipart part numbers (ADR-0080); versitygw
// rejects a part beyond it. The default 0 would reject every part.
const maxMultipartParts = 10000

// Deps configures the s3gateway server (ADR-0080 + ADR-0085).
type Deps struct {
	// BucketFor resolves (namespace, Bucket name) → the substrate bucket view. ok=false
	// when no such Bucket exists for that namespace (⇒ NoSuchBucket / HeadBucket 404).
	BucketFor func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool)
	// PDP is the cedar authorizer; every backend op is a PEP on it (s3::read/s3::write).
	PDP authz.Authorizer
	// Master is the node master secret keypairs are derived from (ADR-0085). Required.
	Master []byte
	// External is the optional external-keypair store (the untrusted SigV4 edge); nil ⇒
	// in-platform only.
	External ExternalKeys
	// Listen is the node-private bind address (e.g. 127.0.0.1:9000).
	Listen string
	// MaxUploadBytes caps a single buffered (multipart/put) object; 0 ⇒ defaultMaxUpload.
	MaxUploadBytes int64
	// Logger is the server logger; nil ⇒ slog.Default(). The master secret is NEVER logged.
	Logger *slog.Logger
}

// Server is the funcd S3 gateway: a versitygw s3api.New server over the funcd backend
// + in-process IAM, bound to a node-private TCP listener (ADR-0085). It is built via
// s3api.New (NOT embedgw.RunVersityGW) so the custom IAM is injectable.
type Server struct {
	api    *s3api.S3ApiServer
	listen string
	log    *slog.Logger

	mu      sync.Mutex
	ready   chan struct{}
	serveCh chan error
	closed  bool
}

// New builds the gateway server (ADR-0085). It validates Deps, constructs the in-process
// IAM + backend, and wires them through s3api.New with a random per-process root account
// (used by nothing external) so versitygw's required-root invariant is satisfied without
// granting anyone root. It does NOT bind a listener until Run is called.
func New(d Deps) (*Server, error) {
	const op = "s3gateway.New"
	if d.BucketFor == nil {
		return nil, fault.Invalidf(op, "BucketFor is required")
	}
	if d.PDP == nil {
		return nil, fault.Invalidf(op, "PDP is required")
	}
	if len(d.Master) == 0 {
		return nil, fault.Invalidf(op, "master secret is required")
	}
	if d.Listen == "" {
		return nil, fault.Invalidf(op, "listen address is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "blob.s3gateway")

	maxUpload := d.MaxUploadBytes
	if maxUpload <= 0 {
		maxUpload = defaultMaxUpload
	}

	backendImpl := &be{
		bucketFor: d.BucketFor,
		pdp:       d.PDP,
		external:  d.External,
		maxUpload: maxUpload,
		log:       logger,
		mp:        newMultipartStore(),
	}
	iamImpl := &iam{master: d.Master, external: d.External}

	rootAccess, rootSecret, err := randomRoot()
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "generate internal root account")
	}
	root := middlewares.RootUserConfig{Access: rootAccess, Secret: rootSecret}

	s := &Server{listen: d.Listen, log: logger, ready: make(chan struct{})}
	// nil for the audit/admin loggers, event sender, and metrics manager — all nil-checked
	// in versitygw's controllers (ADR-0085 verified).
	api, err := s3api.New(backendImpl, root, region, iamImpl, nil, nil, nil, nil,
		// WithConcurrencyLimiter sets the in-flight request cap; versitygw's RateLimiter
		// builds a weighted semaphore of this size, so a zero (the default) rejects EVERY
		// request with 503 SlowDown. A generous cap keeps the node-private gateway open.
		s3api.WithConcurrencyLimiter(0, maxInFlightRequests),
		// MpMaxParts caps multipart part numbers; the default 0 rejects EVERY part
		// (partNumber > 0 fails the bound check). S3's own ceiling is 10000.
		s3api.WithMpMaxParts(maxMultipartParts),
		// DisableACL: funcd authz is entirely the Cedar PEP in the backend; versitygw's
		// ACL grantee model is bypassed so its ownership check reduces to acl.Owner ==
		// caller (which the backend's caller-owned ACL always satisfies).
		s3api.WithDisableACL(),
		s3api.WithOnListen(s.signalReady))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "build s3api server")
	}
	s.api = api
	return s, nil
}

// Run binds the node-private listener and serves until ctx is done (ADR-0085). It is
// opt-in: the daemon only calls it when s3gateway.enabled. Run blocks; callers run it in
// a goroutine. On ctx cancellation it triggers a graceful ShutDown.
func (s *Server) Run(ctx context.Context) error {
	const op = "s3gateway.Run"
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fault.Invalidf(op, "server already closed")
	}
	s.serveCh = make(chan error, 1)
	s.mu.Unlock()

	go func() {
		// ServeMultiPort blocks until the app is shut down; it then returns nil.
		s.serveCh <- s.api.ServeMultiPort([]string{s.listen})
	}()

	select {
	case <-ctx.Done():
		_ = s.Close()
		<-s.serveCh // drain
		return ctx.Err()
	case err := <-s.serveCh:
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "serve s3 gateway")
		}
		return nil
	}
}

// Ready returns a channel closed once the listener is bound and accepting (ADR-0085):
// callers (and tests) wait on it instead of a fixed sleep.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Close gracefully shuts the server down (idempotent).
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.api == nil {
		return nil
	}
	if err := s.api.ShutDown(); err != nil {
		return fault.Wrapf(err, fault.Internal, "s3gateway.Close", "shut down s3 gateway")
	}
	return nil
}

// Addr returns the configured node-private listen address (host:port).
func (s *Server) Addr() string { return s.listen }

func (s *Server) signalReady() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
}

// randomRoot generates a random per-process root keypair (ADR-0085): it is used by
// nothing external — it exists only to satisfy versitygw's required-root invariant.
func randomRoot() (access, secret string, err error) {
	ab, sb := make([]byte, 16), make([]byte, 32)
	if _, err = rand.Read(ab); err != nil {
		return "", "", err
	}
	if _, err = rand.Read(sb); err != nil {
		return "", "", err
	}
	return "FUNCDROOT" + hex.EncodeToString(ab), hex.EncodeToString(sb), nil
}

// LoadOrCreateMaster loads the node S3 master secret from file (ADR-0085), or, when the
// path is empty, from / generates+persists 0600 at <dataDir>/s3gateway/master.key. The
// secret is NEVER logged. A supplied masterSecretFile that does not exist is an error.
func LoadOrCreateMaster(masterSecretFile, dataDir string) ([]byte, error) {
	const op = "s3gateway.LoadOrCreateMaster"
	if masterSecretFile != "" {
		data, err := os.ReadFile(masterSecretFile) //nolint:gosec // operator-supplied secret path
		if err != nil {
			return nil, fault.Invalidf(op, "read master secret %q: %v", masterSecretFile, err)
		}
		if len(data) == 0 {
			return nil, fault.Invalidf(op, "master secret file %q is empty", masterSecretFile)
		}
		return data, nil
	}
	path := filepath.Join(dataDir, "s3gateway", "master.key")
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // daemon-owned data dir
		if len(data) > 0 {
			return data, nil
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "generate master secret")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "create s3gateway dir")
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "persist master secret")
	}
	return secret, nil
}
