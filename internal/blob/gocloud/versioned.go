package gocloud

import (
	"context"
	"errors"
	"maps"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
)

// maxCopyBytes is CopyObject's limit: a larger version cannot be copied over its key in one request.
const maxCopyBytes = 5 << 30

// s3Bucket is an s3:// bucket, the only one with blob.Versioned (ADR-0208): the bucket name and the URL's prefix
// address its versions.
type s3Bucket struct {
	*bucket
	name, prefix string
}

func newS3Bucket(k *bucket, url string) *s3Bucket {
	b := &s3Bucket{bucket: k}
	if u, err := neturl.Parse(url); err == nil {
		b.name, b.prefix = u.Host, u.Query().Get("prefix")
	}
	return b
}

func (k *s3Bucket) client(op string) (*awss3.Client, error) {
	var c *awss3.Client
	if !k.b.As(&c) {
		return nil, fault.Internalf(op, "the s3:// bucket %q has no S3 client", k.name)
	}
	return c, nil
}

// Versioning reads GetBucketVersioning, then GetObjectLockConfiguration (Decision 2): a refused or unimplemented
// versioning read is not Enabled; NoSuchBucket is fault.NotFound; no answer, or a server error after the SDK's
// retries, is fault.Unavailable. Any answer to the lock read but Enabled is no lock.
func (k *s3Bucket) Versioning(ctx context.Context) (blob.Versioning, error) {
	const op = "blob.Versioning"
	c, err := k.client(op)
	if err != nil {
		return blob.Versioning{}, err
	}
	var v blob.Versioning
	out, err := c.GetBucketVersioning(ctx, &awss3.GetBucketVersioningInput{Bucket: aws.String(k.name)})
	switch {
	case err == nil:
		v.Enabled = out.Status == s3types.BucketVersioningStatusEnabled
	case apiCode(err) == "NoSuchBucket":
		return blob.Versioning{}, fault.Wrapf(err, fault.NotFound, op, "bucket %q does not exist", k.name)
	case !answered(err):
		return blob.Versioning{}, fault.Wrapf(err, fault.Unavailable, op, "read the versioning of bucket %q", k.name)
	}
	lock, err := c.GetObjectLockConfiguration(ctx, &awss3.GetObjectLockConfigurationInput{Bucket: aws.String(k.name)})
	switch {
	case err == nil:
		v.ObjectLock = lock.ObjectLockConfiguration != nil &&
			lock.ObjectLockConfiguration.ObjectLockEnabled == s3types.ObjectLockEnabledEnabled
	case !answered(err):
		return blob.Versioning{}, fault.Wrapf(err, fault.Unavailable, op, "read the Object Lock of bucket %q", k.name)
	}
	return v, nil
}

// answered reports an error the store answered: an HTTP status below 500, or 501.
func answered(err error) bool {
	var re *smithyhttp.ResponseError
	if !errors.As(err, &re) || re.Response == nil || re.Response.Response == nil {
		return false
	}
	s := re.HTTPStatusCode()
	return s > 0 && s < http.StatusInternalServerError || s == http.StatusNotImplemented
}

func apiCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// versionEntry is one version or delete marker of a key.
type versionEntry struct {
	id, etag       string
	mod            time.Time
	size           int64
	marker, latest bool
}

// restoreStep is what RestoreAt does to one key: copy from version id, delete (a marker), or nothing.
type restoreStep struct {
	key, id   string
	del, none bool
}

// RestoreAt makes each key under the prefix hold its newest version or delete marker at or before at (Decision
// 6): a differing version is copied over the key, a marker or no version deletes the live key. It plans every key
// before the first write; an error keeps what was done, so a rerun completes it.
func (k *s3Bucket) RestoreAt(ctx context.Context, at time.Time, keep time.Duration) (blob.RestoreReport, error) {
	const op = "blob.RestoreAt"
	now := time.Now()
	switch {
	case keep <= 0:
		return blob.RestoreReport{}, fault.Invalidf(op, "blob.versionRetention is not set: a restore to a time needs the "+
			"days the store's rule keeps a noncurrent version")
	case at.After(now):
		return blob.RestoreReport{}, fault.Invalidf(op, "%s is in the future", at.Format(time.RFC3339))
	case now.Sub(at) > keep:
		return blob.RestoreReport{}, fault.Invalidf(op, "%s is older than blob.versionRetention (%s): the store may have "+
			"expired its versions", at.Format(time.RFC3339), keep)
	}
	v, err := k.Versioning(ctx)
	if err != nil {
		return blob.RestoreReport{}, err
	}
	if !v.Enabled {
		return blob.RestoreReport{}, fault.Invalidf(op, "bucket %q does not keep versions", k.name)
	}
	c, err := k.client(op)
	if err != nil {
		return blob.RestoreReport{}, err
	}
	byKey, err := k.versions(ctx, c)
	if err != nil {
		return blob.RestoreReport{}, err
	}
	var rep blob.RestoreReport
	steps := make([]restoreStep, 0, len(byKey))
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		step, err := plan(key, byKey[key], at)
		if err != nil {
			return blob.RestoreReport{}, err
		}
		if step.id == "" && !step.del {
			rep.Unchanged++
			continue
		}
		steps = append(steps, step)
	}
	for _, s := range steps {
		if s.del {
			if _, err := c.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(k.name), Key: aws.String(s.key)}); err != nil {
				return rep, fault.Wrapf(err, fault.Unavailable, op, "delete %q", s.key)
			}
			rep.Deleted++
			if s.none {
				rep.NoVersion = append(rep.NoVersion, fileUnescapeKey(strings.TrimPrefix(s.key, k.prefix)))
			}
			continue
		}
		src := (&neturl.URL{Path: k.name + "/" + s.key}).EscapedPath() + "?versionId=" + neturl.QueryEscape(s.id)
		if _, err := c.CopyObject(ctx, &awss3.CopyObjectInput{Bucket: aws.String(k.name), Key: aws.String(s.key),
			CopySource: aws.String(src)}); err != nil {
			return rep, fault.Wrapf(err, fault.Unavailable, op, "copy version %s over %q", s.id, s.key)
		}
		rep.Copied++
	}
	return rep, nil
}

// versions lists every version and delete marker under the prefix, by key.
func (k *s3Bucket) versions(ctx context.Context, c *awss3.Client) (map[string][]versionEntry, error) {
	byKey := map[string][]versionEntry{}
	p := awss3.NewListObjectVersionsPaginator(c, &awss3.ListObjectVersionsInput{Bucket: aws.String(k.name), Prefix: aws.String(k.prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Unavailable, "blob.RestoreAt", "list the versions of bucket %q", k.name)
		}
		for _, v := range page.Versions {
			key := aws.ToString(v.Key)
			byKey[key] = append(byKey[key], versionEntry{id: aws.ToString(v.VersionId), etag: aws.ToString(v.ETag),
				mod: aws.ToTime(v.LastModified), size: aws.ToInt64(v.Size), latest: aws.ToBool(v.IsLatest)})
		}
		for _, d := range page.DeleteMarkers {
			key := aws.ToString(d.Key)
			byKey[key] = append(byKey[key], versionEntry{id: aws.ToString(d.VersionId), mod: aws.ToTime(d.LastModified),
				marker: true, latest: aws.ToBool(d.IsLatest)})
		}
	}
	return byKey, nil
}

// plan picks key's newest entry at or before at (the latest one first on a tie) and compares it with the latest: a
// version with the latest's ETag and size is the same content, so a rerun copies nothing again.
func plan(key string, es []versionEntry, at time.Time) (restoreStep, error) {
	var chosen, latest *versionEntry
	for i := range es {
		e := &es[i]
		if e.latest {
			latest = e
		}
		if e.mod.After(at) {
			continue
		}
		if chosen == nil || e.mod.After(chosen.mod) || e.mod.Equal(chosen.mod) && e.latest {
			chosen = e
		}
	}
	liveGone := latest == nil || latest.marker
	switch {
	case chosen == nil:
		return restoreStep{key: key, del: !liveGone, none: true}, nil
	case chosen.marker:
		return restoreStep{key: key, del: !liveGone}, nil
	case latest != nil && !latest.marker && (chosen.id == latest.id || chosen.etag == latest.etag && chosen.size == latest.size):
		return restoreStep{key: key}, nil
	case chosen.size > maxCopyBytes:
		return restoreStep{}, fault.Invalidf("blob.RestoreAt", "version %s of %q is %d bytes, over CopyObject's 5 GiB: "+
			"nothing was restored", chosen.id, key, chosen.size)
	}
	return restoreStep{key: key, id: chosen.id}, nil
}
