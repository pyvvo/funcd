// Package blob is the blob storage service (ADR-0021, F23): the function-facing Facade
// (PDP-authorized, <namespace>/<binding>/<key>-prefixed) over the blob.Bucket port
// (ADR-0007), plus the blob services.TypeHandler the Service dispatcher routes type:blob
// to. It is the first reuse of the ADR-0019 service facade pattern (it copies KV's shape;
// only the driver — gocloud blob — differs).
package blob

import (
	"context"
	"log/slog"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/services"
)

// FacadeDeps configures the blob facade (the PEP).
type FacadeDeps struct {
	Bucket     blob.Bucket
	Authorizer auth.Authorizer
	Logger     *slog.Logger
}

// Facade is what a function calls: PDP-authorized + namespace/binding-prefixed blob storage.
type Facade struct {
	bucket blob.Bucket
	authz  auth.Authorizer
	logger *slog.Logger
}

// NewFacade builds the blob facade. Bucket + Authorizer are required.
func NewFacade(d FacadeDeps) (*Facade, error) {
	if d.Bucket == nil {
		return nil, fault.Invalidf("services.blob.NewFacade", "bucket is required")
	}
	if d.Authorizer == nil {
		return nil, fault.Invalidf("services.blob.NewFacade", "authorizer is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Facade{bucket: d.Bucket, authz: d.Authorizer, logger: logger.With("component", "services.blob")}, nil
}

func (f *Facade) authorize(ctx context.Context, id auth.Identity, verb auth.Verb, ns v1.NamespaceName) error {
	dec, err := f.authz.Authorize(ctx, auth.Request{Identity: id, Verb: verb, Kind: v1.KindService, Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "services.blob.authz", "authorize")
	}
	if !dec.Allowed {
		return fault.Forbiddenf("services.blob.authz", "blob %s in %q denied: %s", verb, ns, dec.Reason)
	}
	return nil
}

func tenantKey(ns v1.NamespaceName, binding, key string) string {
	return string(ns) + "/" + binding + "/" + key
}

// Get returns the object's bytes (PDP-authorized).
func (f *Facade) Get(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) ([]byte, error) {
	if err := f.authorize(ctx, id, auth.VerbGet, ns); err != nil {
		return nil, err
	}
	return f.bucket.Get(ctx, tenantKey(ns, binding, key))
}

// Put stores the object (PDP-authorized).
func (f *Facade) Put(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string, data []byte) error {
	if err := f.authorize(ctx, id, auth.VerbUpdate, ns); err != nil {
		return err
	}
	return f.bucket.Put(ctx, tenantKey(ns, binding, key), data)
}

// Delete removes the object (PDP-authorized).
func (f *Facade) Delete(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) error {
	if err := f.authorize(ctx, id, auth.VerbDelete, ns); err != nil {
		return err
	}
	return f.bucket.Delete(ctx, tenantKey(ns, binding, key))
}

// List returns the binding's keys under prefix, tenant-prefix stripped.
func (f *Facade) List(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, prefix string) ([]string, error) {
	if err := f.authorize(ctx, id, auth.VerbList, ns); err != nil {
		return nil, err
	}
	tenantPrefix := string(ns) + "/" + binding + "/"
	attrs, err := f.bucket.List(ctx, tenantPrefix+prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(attrs))
	for i, a := range attrs {
		out[i] = strings.TrimPrefix(a.Key, tenantPrefix)
	}
	return out, nil
}

// SignedURL returns a presigned URL for the object. It authorizes the CAPABILITY the URL
// grants (derived from opts.Method) — a presigned PUT/DELETE bypasses the facade to
// write/delete, so it requires the write verb (no read→write escalation).
func (f *Facade) SignedURL(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string, opts blob.SignOptions) (string, error) {
	if err := f.authorize(ctx, id, verbForSign(opts.Method), ns); err != nil {
		return "", err
	}
	return f.bucket.SignedURL(ctx, tenantKey(ns, binding, key), opts)
}

// verbForSign maps the granted sign method to the authz verb (a zero method is SignGet).
func verbForSign(m blob.SignMethod) auth.Verb {
	switch m {
	case blob.SignPut:
		return auth.VerbUpdate
	case blob.SignDelete:
		return auth.VerbDelete
	default: // SignGet or zero
		return auth.VerbGet
	}
}

// --- the Service dispatcher's blob TypeHandler ---

type handler struct{}

// NewHandler returns the blob services.TypeHandler (Type() == ServiceTypeBlob).
func NewHandler() services.TypeHandler { return handler{} }

func (handler) Type() v1.ServiceType { return v1.ServiceTypeBlob }

// Reconcile validates the blob binding spec; the pooled bucket needs no external
// provisioning, so a valid binding is immediately Ready (the dispatcher writes status).
func (handler) Reconcile(_ context.Context, svc *v1.Service) (controller.Result, error) {
	if svc.Spec.Blob == nil || svc.Spec.Blob.Binding == "" {
		return controller.Result{}, fault.Invalidf("services.blob.Reconcile", "blob service %q requires spec.blob.binding", svc.Name)
	}
	return controller.Result{}, nil
}
