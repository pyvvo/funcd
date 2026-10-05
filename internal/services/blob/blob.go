// Package blob is the blob storage service: the function-facing, binding-gated Facade (ADR-0127) over the
// blob.Bucket substrate, plus the blob services.TypeHandler the Service dispatcher routes type:blob to.
//
// ADR-0127 makes context.blob the blob twin of context.kv (ADR-0069): blob bindings live on the consumer
// (Function.spec.blob), and authorization is the PDP's job. The facade resolves a caller's (function,
// alias) to a (bucket, prefix) — that is NAMING (ADR-0073 bind-as-grant, default-deny) — then asks the PDP
// the SAME per-object question the S3 frontend asks (ADR-0080's S3Capability): s3::read (get/list/sign-GET)
// or s3::write (put/delete/sign-PUT/DELETE) on the bound BlobPrefix, with the connection-scoped caller
// Function as the principal (whose spec.blob the capability materializes as its blobBindings). It acts on
// the SAME s3BucketFor substrate view under the SAME blobKey keyspace as the frontend, so context.blob
// objects and the S3-frontend objects are one and the same. A PDP deny maps to fault.Forbidden.
//
// This supersedes the unused legacy ADR-0021 NewFacade (which authorized Kind:Service with no per-object
// Action — an RBAC-role question that could not enforce spec.blob bind-as-grant). The live Service-
// dispatcher TypeHandler is unchanged.
package blob

import (
	"context"
	"log/slog"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/services"
)

// BucketResolver maps an (ns, bucket-name) to a prefixed substrate view — the s3gateway BucketFor
// (pkg/funcd's s3BucketFor). A miss (no such bucket) ⇒ the facade returns fault.NotFound.
type BucketResolver func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool)

// FacadeDeps configures the blob facade (the PEP). The Resolver (ADR-0127/0073) resolves the
// alias→(bucket,prefix) NAMING; the BucketFor maps (ns,bucket)→substrate; the Authorizer (ADR-0080
// S3Capability) is the PDP that decides s3::read/s3::write.
type FacadeDeps struct {
	Resolver   BindingResolver
	BucketFor  BucketResolver
	Authorizer auth.Authorizer
	Logger     *slog.Logger
}

// Facade is what a function calls via context.blob: binding-resolved + PDP-authorized + prefix-keyed blob
// storage (ADR-0127). It satisfies the worker-node local API's Blob port directly — it builds the Function
// principal internally, so no adapter is needed.
type Facade struct {
	resolver   BindingResolver
	bucketFor  BucketResolver
	authorizer auth.Authorizer
	logger     *slog.Logger
}

// NewFacade builds the blob facade. Resolver, BucketFor, and Authorizer are required.
func NewFacade(d FacadeDeps) (*Facade, error) {
	const op = "services.blob.NewFacade"
	if d.Resolver == nil {
		return nil, fault.Invalidf(op, "resolver is required")
	}
	if d.BucketFor == nil {
		return nil, fault.Invalidf(op, "bucket resolver is required")
	}
	if d.Authorizer == nil {
		return nil, fault.Invalidf(op, "authorizer is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Facade{resolver: d.Resolver, bucketFor: d.BucketFor, authorizer: d.Authorizer, logger: logger.With("component", "services.blob")}, nil
}

// blobKey is the substrate key for a (prefix, object) within a bucket — the prefix sub-domain is
// preserved so distinct prefixes stay isolated. IDENTICAL to the s3gateway keyspace (ADR-0080/0127), so
// context.blob objects are the same objects the S3 frontend serves.
func blobKey(prefix, object string) string {
	if object == "" {
		return prefix
	}
	return prefix + "/" + object
}

// authorize asks the PDP the SAME per-object question the S3 frontend asks (ADR-0080 S3Capability): the
// action (s3::read | s3::write) on the bound BlobPrefix (a KindBucket resource with the prefix as its
// sub-resource Path). The principal is the connection-scoped caller Function, whose spec.blob the
// capability materializes as its blobBindings — so an unbound alias (or a non-owner write) is denied. A
// deny maps to fault.Forbidden.
func (f *Facade) authorize(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, action auth.Action, b Binding) error {
	const op = "services.blob.authorize"
	principal := &auth.EntityRef{Type: v1.KindFunction, Namespace: ns, Name: fn}
	resource := &auth.EntityRef{Type: v1.KindBucket, Namespace: ns, Name: b.Bucket, Path: b.Prefix}
	dec, err := f.authorizer.Authorize(ctx, auth.Request{
		Identity: auth.Identity{Subject: string(ns) + "/" + string(fn), Principal: principal},
		Action:   action,
		Resource: resource,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "authorize %s on prefix %q/%q", action, b.Bucket, b.Prefix)
	}
	if !dec.Allowed {
		return fault.Forbiddenf(op, "function %q is not authorized to %s prefix %q (bucket %q): %s", fn, action, b.Prefix, b.Bucket, dec.Reason)
	}
	return nil
}

// resolveAuth resolves a binding (naming, default-deny) then authorizes the action against its
// BlobPrefix via the PDP, returning the binding + its substrate bucket view on allow.
func (f *Facade) resolveAuth(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string, action auth.Action) (Binding, blob.Bucket, error) {
	const op = "services.blob.resolveAuth"
	b, err := f.resolver.Resolve(ctx, ns, fn, alias)
	if err != nil {
		return Binding{}, nil, err
	}
	if err := f.authorize(ctx, ns, fn, action, b); err != nil {
		return Binding{}, nil, err
	}
	sub, ok := f.bucketFor(ns, string(b.Bucket))
	if !ok {
		return Binding{}, nil, fault.NotFoundf(op, "binding %q references missing bucket %q", alias, b.Bucket)
	}
	return b, sub, nil
}

// Get returns the object's bytes for the alias's key (found=false on a missing object). The PDP
// authorizes s3::read on the bound prefix (ADR-0127).
func (f *Facade) Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) ([]byte, bool, error) {
	b, sub, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionS3Read)
	if err != nil {
		return nil, false, err
	}
	data, err := sub.Get(ctx, blobKey(b.Prefix, key))
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

// Put stores data under the alias's key. The PDP authorizes s3::write — the built-in prefix-owner rule
// makes writes owner-only (ADR-0080/0127).
func (f *Facade) Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, data []byte) error {
	b, sub, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionS3Write)
	if err != nil {
		return err
	}
	return sub.Put(ctx, blobKey(b.Prefix, key), data)
}

// Delete removes the alias's key. The PDP authorizes s3::write (owner-only).
func (f *Facade) Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) error {
	b, sub, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionS3Write)
	if err != nil {
		return err
	}
	return sub.Delete(ctx, blobKey(b.Prefix, key))
}

// List returns the alias's keys under prefix. Like Get, it authorizes s3::read (ADR-0127). Keys are
// returned relative to the bound prefix (the prefix sub-domain is stripped) so the caller sees only its
// own key space.
func (f *Facade) List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, prefix string) ([]string, error) {
	b, sub, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionS3Read)
	if err != nil {
		return nil, err
	}
	strip := blobKey(b.Prefix, "") + "/"
	attrs, err := sub.List(ctx, strip+prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(attrs))
	for i, a := range attrs {
		out[i] = strings.TrimPrefix(a.Key, strip)
	}
	return out, nil
}

// SignedURL returns a substrate-driver presigned URL for the object. It authorizes the CAPABILITY the URL
// grants (derived from opts.Method): a presigned PUT/DELETE bypasses the facade to write/delete, so it
// requires s3::write (no read→write escalation); a GET requires s3::read (ADR-0127).
func (f *Facade) SignedURL(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, opts blob.SignOptions) (string, error) {
	b, sub, err := f.resolveAuth(ctx, ns, fn, alias, actionForSign(opts.Method))
	if err != nil {
		return "", err
	}
	return sub.SignedURL(ctx, blobKey(b.Prefix, key), opts)
}

// actionForSign maps the granted sign method to the S3 authz action (a zero method is SignGet).
func actionForSign(m blob.SignMethod) auth.Action {
	switch m {
	case blob.SignPut, blob.SignDelete:
		return auth.ActionS3Write
	default: // SignGet or zero
		return auth.ActionS3Read
	}
}

// --- the Service dispatcher's blob TypeHandler (unchanged, ADR-0021) ---

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
