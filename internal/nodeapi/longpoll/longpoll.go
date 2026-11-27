// Package longpoll is the node API port's HTTPS long-poll driver (ADR-0365 Decision 2): HTTPS and JSON on one
// listener, on the API's own stack (chi and huma), with mTLS for every route but the join. A node long-polls the change log
// and its call queue, and the control plane never dials it. It is the production driver and the test driver alike.
package longpoll

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

// readHeaderTimeout bounds a node's request header, as on the platform's own listeners, and bodyReadTimeout its body.
const (
	readHeaderTimeout = 10 * time.Second
	bodyReadTimeout   = 30 * time.Second
)

// The node API's paths.
const (
	pathJoin     = "/node/v1/join"
	pathSnapshot = "/node/v1/snapshot"
	pathFeed     = "/node/v1/feed"
	pathStatus   = "/node/v1/status"
	pathObjects  = "/node/v1/objects"
	pathCalls    = "/node/v1/calls"
	pathKeys     = "/node/v1/keys"
	pathReport   = "/node/v1/report"
	pathSecrets  = "/node/v1/secrets"
	pathChildren = "/node/v1/children"
	pathRenew    = "/node/v1/renew"
)

// The headers of a node's request (ADR-0374): the node API version it speaks, its release and its platform.
const (
	headerAPIVersion = "Funcd-Node-Api"
	headerVersion    = "Funcd-Node-Version"
	headerPlatform   = "Funcd-Node-Platform"
)

// Config configures the control plane's side.
type Config struct {
	Addr   string      // where the node API listens
	TLS    *tls.Config // the node API's certificate, and the CA that signs the nodes' (a client certificate is verified if given)
	Logger *slog.Logger
	Now    func() time.Time // the call queue's clock; nil ⇒ time.Now
}

type server struct {
	cfg   Config
	srv   *http.Server
	ln    net.Listener
	calls *calls
}

// Listen binds the node API on cfg.Addr.
func Listen(cfg Config) (nodeapi.Server, error) {
	const op = "longpoll.Listen"
	if cfg.TLS == nil {
		return nil, fault.Invalidf(op, "the node API's TLS is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "bind the node API on %s", cfg.Addr)
	}
	return &server{cfg: cfg, ln: ln, calls: newCalls(cfg.Now), srv: &http.Server{
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
		TLSConfig:         cfg.TLS,
	}}, nil
}

func (s *server) Addr() string { return s.ln.Addr().String() }

// Serve serves until ctx ends, then closes every connection, long polls included.
func (s *server) Serve(ctx context.Context, h nodeapi.Handler) error {
	s.srv.Handler = routes(h, s.calls, s.cfg.Logger)
	go func() {
		<-ctx.Done()
		_ = s.srv.Close()
	}()
	if err := s.srv.ServeTLS(s.ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *server) Close() error { return s.srv.Close() }

func (s *server) Call(ctx context.Context, node v1.ObjectName, c nodeapi.Call) (nodeapi.Answer, error) {
	return s.calls.do(ctx, node, c)
}

// api answers the node API's routes with h, and the call queue's with calls.
type api struct {
	h     nodeapi.Handler
	calls *calls
}

// routes are the node API on the API's own stack (decision 9): the join, then the routes of a joined node.
func routes(h nodeapi.Handler, calls *calls, logger *slog.Logger) http.Handler {
	a := &api{h: h, calls: calls}
	r := chi.NewRouter()
	r.Use(fault.LogCauses(logger), apiVersion)
	handle(nodeAPI(r), op(http.MethodPost, pathJoin, nodeapi.MaxJoinBody), a.join)
	r.Group(func(r chi.Router) {
		r.Use(a.authNode)
		api := nodeAPI(r)
		handle(api, op(http.MethodPost, pathRenew, nodeapi.MaxJoinBody), a.renew)
		handle(api, op(http.MethodPut, pathRenew, 0), a.renewed)
		handle(api, op(http.MethodGet, pathSnapshot, 0), a.snapshot)
		handle(api, op(http.MethodGet, pathFeed, 0), a.feed)
		handle(api, op(http.MethodGet, pathChildren, 0), a.children)
		handle(api, op(http.MethodPut, pathStatus, nodeapi.MaxStatusBody), a.status)
		handle(api, op(http.MethodPut, pathObjects, nodeapi.MaxObjectBody), a.putObject)
		handle(api, op(http.MethodDelete, pathObjects, 0), a.deleteObject)
		handle(api, op(http.MethodPut, pathReport, nodeapi.MaxStatusBody), a.report)
		handle(api, op(http.MethodGet, pathKeys, 0), a.keys)
		handle(api, op(http.MethodPost, pathSecrets, nodeapi.MaxObjectBody), a.secret)
		handle(api, op(http.MethodGet, pathCalls, 0), a.poll)
		handle(api, op(http.MethodPost, pathCalls+"/{id}", nodeapi.MaxObjectBody), a.answer)
	})
	return r
}

func nodeAPI(r chi.Router) huma.API {
	return controlplane.NewInnerAPI(r, "funcd node API", strconv.Itoa(nodeapi.APIVersion))
}

// op is the operation of method on path, whose body is at most limit bytes.
func op(method, path string, limit int64) huma.Operation {
	return huma.Operation{OperationID: method + " " + path, Method: method, Path: path, MaxBodyBytes: limit,
		BodyReadTimeout: bodyReadTimeout}
}

// handle registers h, which answers with the fault of its error.
func handle[I, O any](api huma.API, op huma.Operation, h func(context.Context, *I) (*O, error)) { //nolint:forbidigo // huma's own constraint
	huma.Register(api, op, func(ctx context.Context, in *I) (*O, error) {
		out, err := h(ctx, in)
		if err != nil {
			return nil, controlplane.FaultError(err)
		}
		return out, nil
	})
}

type nodeKey struct{}

func nodeOf(ctx context.Context) v1.ObjectName {
	n, _ := ctx.Value(nodeKey{}).(v1.ObjectName)
	return n
}

// authNode lets a joined node's request in: a node API version this control plane does not serve gets 426, a request
// with no certificate the CA verified 401, and the Handler's Admit decides the rest, as the node the certificate names.
func (a *api) authNode(next http.Handler) http.Handler {
	const op = "longpoll.authNode"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version, _ := strconv.Atoi(r.Header.Get(headerAPIVersion)) // apiVersion refused any other
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			fault.WriteProblem(w, fault.Unauthorizedf(op, "a node certificate is required"))
			return
		}
		leaf := r.TLS.PeerCertificates[0]
		p := nodeapi.Peer{Node: v1.ObjectName(leaf.Subject.CommonName), Serial: leaf.SerialNumber.Text(16), APIVersion: version,
			Version: r.Header.Get(headerVersion), Platform: r.Header.Get(headerPlatform)}
		if err := a.h.Admit(r.Context(), p); err != nil {
			fault.WriteProblem(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nodeKey{}, p.Node)))
	})
}

// apiVersion answers 426 to a request, the join's included, whose node API version this control plane does not serve,
// before anything else reads it (ADR-0374 Decision 1).
func apiVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(headerAPIVersion)
		if v, err := strconv.Atoi(got); err != nil || !nodeapi.Supported(v) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUpgradeRequired)
			_ = json.NewEncoder(w).Encode(fault.Problem{Type: "about:blank", Title: http.StatusText(http.StatusUpgradeRequired),
				Status: http.StatusUpgradeRequired, Detail: nodeapi.Unserved(got)})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type joinInput struct{ Body nodeapi.JoinRequest }
type joinOutput struct{ Body nodeapi.JoinResponse }

func (a *api) join(ctx context.Context, in *joinInput) (*joinOutput, error) {
	out, err := a.h.Join(ctx, in.Body)
	if err != nil {
		return nil, err
	}
	return &joinOutput{Body: out}, nil
}

type renewInput struct{ Body nodeapi.RenewRequest }

func (a *api) renew(ctx context.Context, in *renewInput) (*joinOutput, error) {
	out, err := a.h.Renew(ctx, nodeOf(ctx), in.Body)
	if err != nil {
		return nil, err
	}
	return &joinOutput{Body: out}, nil
}

// renewed answers a node's first request under its renewed certificate, which Admit made its certificate.
func (a *api) renewed(context.Context, *struct{}) (*struct{}, error) { return nil, nil }

type snapshotInput struct {
	Namespace v1.NamespaceName `query:"namespace"`
}
type snapshotOutput struct{ Body nodeapi.Snapshot }

func (a *api) snapshot(ctx context.Context, in *snapshotInput) (*snapshotOutput, error) {
	out, err := a.h.Snapshot(ctx, nodeOf(ctx), in.Namespace)
	if err != nil {
		return nil, err
	}
	return &snapshotOutput{Body: out}, nil
}

// WaitInput is a long poll's wait, a duration; one left out, or out of bounds, is nodeapi.DefaultWait. It is exported
// because huma reads no field of an unexported embedded struct.
type WaitInput struct {
	Wait string `query:"wait"`
}

func (w WaitInput) duration() time.Duration {
	d, _ := time.ParseDuration(w.Wait)
	return nodeapi.Wait(d)
}

type feedInput struct {
	WaitInput
	Since    uint64 `query:"since" required:"true"`
	Timeline string `query:"timeline"`
}
type feedOutput struct{ Body nodeapi.Feed }

// feed answers GET …/feed?since=&timeline=&wait= (ADR-0365 Decision 4): the node's records after since, as soon as there is one, or
// none once wait passes. A revision the Handler no longer serves is 410: the node starts again from a snapshot.
func (a *api) feed(ctx context.Context, in *feedInput) (*feedOutput, error) {
	f, err := nodeapi.WaitForChanges(ctx, a.h, nodeOf(ctx), in.Since, in.Timeline, in.duration())
	if fault.KindOf(err) == fault.PreconditionFailed {
		return nil, huma.Error410Gone(err.Error())
	}
	if err != nil {
		return nil, err
	}
	return &feedOutput{Body: f}, nil
}

type childrenOutput struct{ Body []store.Change }

func (a *api) children(ctx context.Context, _ *struct{}) (*childrenOutput, error) {
	out, err := a.h.Children(ctx, nodeOf(ctx))
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []store.Change{}
	}
	return &childrenOutput{Body: out}, nil
}

type statusInput struct{ Body nodeapi.StatusWrite }

func (a *api) status(ctx context.Context, in *statusInput) (*struct{}, error) {
	return nil, a.h.Status(ctx, nodeOf(ctx), in.Body)
}

type objectInput struct{ Body nodeapi.ObjectWrite }

func (a *api) putObject(ctx context.Context, in *objectInput) (*struct{}, error) {
	return nil, a.h.PutObject(ctx, nodeOf(ctx), in.Body)
}

type deleteInput struct {
	Kind      v1.Kind          `query:"kind" required:"true"`
	Namespace v1.NamespaceName `query:"namespace" required:"true"`
	Name      v1.ObjectName    `query:"name" required:"true"`
	UID       v1.UID           `query:"uid" required:"true"`
}

func (a *api) deleteObject(ctx context.Context, in *deleteInput) (*struct{}, error) {
	return nil, a.h.DeleteObject(ctx, nodeOf(ctx), nodeapi.ObjectRef{Kind: in.Kind, Namespace: in.Namespace, Name: in.Name, UID: in.UID})
}

type reportInput struct{ Body nodeapi.NodeReport }

func (a *api) report(ctx context.Context, in *reportInput) (*struct{}, error) {
	return nil, a.h.Report(ctx, nodeOf(ctx), in.Body)
}

type keysInput struct {
	Namespace v1.NamespaceName `query:"namespace" required:"true"`
}
type keysOutput struct{ Body nodeapi.NamespaceKey }

func (a *api) keys(ctx context.Context, in *keysInput) (*keysOutput, error) {
	out, err := a.h.Key(ctx, nodeOf(ctx), in.Namespace)
	if err != nil {
		return nil, err
	}
	return &keysOutput{Body: out}, nil
}

type secretInput struct{ Body nodeapi.SecretWrite }
type secretOutput struct{ Body nodeapi.SecretAnswer }

func (a *api) secret(ctx context.Context, in *secretInput) (*secretOutput, error) {
	out, err := a.h.Secret(ctx, nodeOf(ctx), in.Body)
	if err != nil {
		return nil, err
	}
	return &secretOutput{Body: out}, nil
}

type callsOutput struct{ Body []nodeapi.Call }

// poll answers GET …/calls?wait=: the node's queued calls, none once wait passes.
func (a *api) poll(ctx context.Context, in *WaitInput) (*callsOutput, error) {
	return &callsOutput{Body: a.calls.poll(ctx, nodeOf(ctx), in.duration())}, nil
}

type answerInput struct {
	ID   string `path:"id"`
	Body nodeapi.Answer
}

// answer answers POST …/calls/{id}: the node's answer reaches the waiting caller.
func (a *api) answer(ctx context.Context, in *answerInput) (*struct{}, error) {
	return nil, a.calls.answer(nodeOf(ctx), in.ID, in.Body)
}
