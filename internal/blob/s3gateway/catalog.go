package s3gateway

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/versity/versitygw/s3err"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	authz "github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob"
)

// catalogName ends every key of the catalog store (ADR-0293).
const catalogName = "/_ducklake/catalog.db"

// object resolves the bucket view of an object read or write: the catalog store under v1.LakeCatalogBucket by its own
// rule, any other bucket through the Cedar PEP.
func (b *be) object(ctx context.Context, action authz.Action, bucket, key string) (blob.Bucket, principal, error) {
	if bucket == v1.LakeCatalogBucket {
		return b.catalog(ctx, key)
	}
	prefix, _ := splitKey(key)
	return b.authorize(ctx, action, bucket, prefix)
}

// catalog serves a key of the catalog store to one principal (ADR-0293 Decision 2): the Lake that owns the prefix of
// the Bucket the key names, <bucket>/<prefix>/_ducklake/catalog.db, in its namespace. No Cedar policy is asked; any
// other caller or key is AccessDenied.
func (b *be) catalog(ctx context.Context, key string) (blob.Bucket, principal, error) {
	pr, err := b.caller(ctx)
	if err != nil {
		return nil, principal{}, err
	}
	if b.catalogs == nil || pr.ref.Type != v1.KindLake {
		return nil, pr, accessDenied()
	}
	bucket, prefix, ok := catalogLocation(key)
	if !ok {
		return nil, pr, accessDenied()
	}
	all, err := b.namespaceBuckets(ctx, pr)
	if err != nil {
		return nil, pr, err
	}
	owner := v1.LakeOwner(pr.ref.Name)
	owns := slices.ContainsFunc(all, func(bk v1.Bucket) bool {
		return string(bk.Name) == bucket && slices.ContainsFunc(bk.Spec.Prefixes, func(p v1.BucketPrefix) bool {
			return p.Name == prefix && p.Owner == owner
		})
	})
	if !owns {
		return nil, pr, accessDenied()
	}
	return b.catalogs(pr.namespace), pr, nil
}

// catalogLocation splits a catalog key, <bucket>/<prefix>/_ducklake/catalog.db, ok=false for any other key.
func catalogLocation(key string) (bucket, prefix string, ok bool) {
	loc, ok := strings.CutSuffix(key, catalogName)
	bucket, prefix, split := strings.Cut(loc, "/")
	return bucket, prefix, ok && split && !strings.Contains(prefix, "/")
}

// deletable resolves the view a delete of key runs on: the catalog store by its own rule, any other bucket through the
// PEP's s3::write.
func (b *be) deletable(ctx context.Context, bucket, key string) (blob.Bucket, principal, error) {
	if bucket == v1.LakeCatalogBucket {
		return b.catalogDelete(ctx, key)
	}
	prefix, _ := splitKey(key)
	return b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
}

// catalogDelete serves the delete of a catalog (ADR-0293 Decision 6): to a caller the PDP lets write the prefix the key
// names, while no Lake of its namespace is located there, so a deleted Lake's catalog can be drained from its Bucket.
func (b *be) catalogDelete(ctx context.Context, key string) (blob.Bucket, principal, error) {
	pr, err := b.caller(ctx)
	if err != nil {
		return nil, principal{}, err
	}
	bucket, prefix, ok := catalogLocation(key)
	if !ok || b.catalogs == nil || b.lakeAt == nil {
		return nil, pr, accessDenied()
	}
	allowed, err := b.allowed(ctx, pr, authz.ActionS3Write, bucket, prefix)
	if err != nil {
		return nil, principal{}, err
	}
	if !allowed {
		return nil, pr, accessDenied()
	}
	lake, located, err := b.lakeAt(ctx, pr.namespace, bucket, prefix)
	if err != nil {
		b.log.Error("s3 catalog delete: find the Lake", "namespace", pr.namespace, "bucket", bucket, "prefix", prefix, "err", err)
		return nil, pr, s3err.GetAPIError(s3err.ErrInternalError)
	}
	if located {
		return nil, pr, s3err.APIError{
			Code:           "AccessDenied",
			Description:    fmt.Sprintf("Lake %s is located on %s/%s: its catalog is deleted only once the Lake is", lake, bucket, prefix),
			HTTPStatusCode: http.StatusForbidden,
		}
	}
	return b.catalogs(pr.namespace), pr, nil
}

// catalogTooLarge is the refusal of a catalog over lake.catalogMaxSize (ADR-0293 Decision 3).
func (b *be) catalogTooLarge(size int) error {
	if int64(size) <= b.catalogMax || b.catalogMax <= 0 {
		return nil
	}
	return s3err.APIError{
		Code:           "EntityTooLarge",
		Description:    fmt.Sprintf("the catalog is %d bytes, over lake.catalogMaxSize (%d bytes)", size, b.catalogMax),
		HTTPStatusCode: http.StatusBadRequest,
	}
}
