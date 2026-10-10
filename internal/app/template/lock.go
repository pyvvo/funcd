package template

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
)

const (
	lockOp   = "app.lock"
	lockFile = "app.lock"
)

var digestForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// LockedImage is one image of app.lock (ADR-0218 Decision 3).
type LockedImage struct {
	Requested string `json:"requested"` // the images entry as written; a mismatch makes the lock stale
	Version   string `json:"version"`   // the tag it resolved to
	Digest    string `json:"digest"`    // sha256:<64 hex>
}

// lockDoc is the app.lock file.
type lockDoc struct {
	Images map[string]LockedImage `json:"images"`
}

// ImageResolver reads an image registry for Lock and Moved.
type ImageResolver interface {
	Tags(ctx context.Context, repo string) ([]string, error) // every tag of <registry>/<repo>
	Digest(ctx context.Context, ref string) (string, error)  // the manifest digest of <registry>/<repo>:<tag>
}

// Lock resolves every images entry at registry, in name order (ADR-0218 Decision 2): an exact entry to the
// digest of its tag; a range to the highest tag that StrictNewVersion parses and the range accepts, then its digest.
// It returns nothing on any failure.
func Lock(ctx context.Context, t *Template, registry string, r ImageResolver) (map[string]LockedImage, error) {
	out := make(map[string]LockedImage, len(t.Images))
	for _, name := range slices.Sorted(maps.Keys(t.Images)) {
		img, err := t.parsed(name)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, lockOp, "images.%s", name)
		}
		repo, version := registry+"/"+img.Repo, img.Exact
		if img.Range != nil {
			tags, err := r.Tags(ctx, repo)
			if err != nil {
				return nil, fault.Wrapf(err, fault.KindOf(err), lockOp, "images.%s: list the tags of %s", name, repo)
			}
			if version = highest(tags, img.Range); version == "" {
				_, spec, _ := strings.Cut(t.Images[name], ":")
				return nil, fault.NotFoundf(lockOp, "images.%s: no tag of %s satisfies the range %s", name, repo, spec)
			}
		}
		digest, err := r.Digest(ctx, repo+":"+version)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), lockOp, "images.%s: resolve %s:%s", name, repo, version)
		}
		if !digestForm.MatchString(digest) {
			return nil, fault.Invalidf(lockOp, "images.%s: %s:%s resolves to %s, not a sha256 digest", name, repo, version, digest)
		}
		out[name] = LockedImage{Requested: t.Images[name], Version: version, Digest: digest}
	}
	return out, nil
}

// highest is the highest tag that StrictNewVersion parses and the range accepts, "" when none does. A pre-release
// is accepted only when the range names one (the library default).
func highest(tags []string, c *semver.Constraints) string {
	var best *semver.Version
	for _, tag := range tags {
		v, err := semver.StrictNewVersion(tag)
		if err != nil || !c.Check(v) {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best = v
		}
	}
	if best == nil {
		return ""
	}
	return best.Original()
}

// CheckLock refuses a template without a lock, or with a stale one: a lock whose names differ from the images
// names, or one of whose requested differs from its images entry. It names the first such image in name order.
func CheckLock(t *Template) error {
	if t.Lock == nil {
		return fault.Invalidf(lockOp, "the template has no %s: run funcdctl app lock", lockFile)
	}
	names := slices.Sorted(maps.Keys(t.Images))
	for name := range t.Lock {
		if _, ok := t.Images[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		entry, inImages := t.Images[name]
		locked, inLock := t.Lock[name]
		if inImages && inLock && entry == locked.Requested {
			continue
		}
		return fault.Invalidf(lockOp, "%s is stale: images.%s is %s and the lock records %s: run funcdctl app lock",
			lockFile, name, orNone(entry, inImages), orNone(locked.Requested, inLock))
	}
	return nil
}

func orNone(s string, ok bool) string {
	if !ok {
		return "nothing"
	}
	return fmt.Sprintf("%q", s)
}

// ReadLock reads <dir>/app.lock strictly; nil, nil when it is absent.
func ReadLock(dir string) (map[string]LockedImage, error) {
	data, err := os.ReadFile(filepath.Join(dir, lockFile)) //nolint:gosec // the template directory is the caller's argument
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Invalidf(lockOp, "read %s: %v", lockFile, err)
	}
	var doc lockDoc
	if err := yaml.UnmarshalStrict(data, &doc); err != nil {
		return nil, fault.Invalidf(lockOp, "%s: %v", lockFile, err)
	}
	for _, name := range slices.Sorted(maps.Keys(doc.Images)) {
		if l := doc.Images[name]; !digestForm.MatchString(l.Digest) {
			return nil, fault.Invalidf(lockOp, "%s: images.%s: digest %q is not sha256: and 64 hex digits", lockFile, name, l.Digest)
		}
	}
	if doc.Images == nil {
		doc.Images = map[string]LockedImage{}
	}
	return doc.Images, nil
}

// WriteLock writes <dir>/app.lock with sorted keys, through a temporary file and a rename, so the same resolution
// writes the same bytes and a reader never sees half a lock.
func WriteLock(dir string, lock map[string]LockedImage) error {
	data, err := yaml.Marshal(lockDoc{Images: lock})
	if err != nil {
		return fault.Internalf(lockOp, "marshal %s: %v", lockFile, err)
	}
	tmp, err := os.CreateTemp(dir, "."+lockFile+"-*")
	if err != nil {
		return fault.Invalidf(lockOp, "write %s: %v", lockFile, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fault.Invalidf(lockOp, "write %s: %v", lockFile, err)
	}
	if err := tmp.Close(); err != nil {
		return fault.Invalidf(lockOp, "write %s: %v", lockFile, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, lockFile)); err != nil {
		return fault.Invalidf(lockOp, "write %s: %v", lockFile, err)
	}
	return nil
}

// Moved resolves each locked <registry>/<repo>:<version> once and returns a warning for each tag that now names
// another digest, and for each check that failed (ADR-0218 Decision 5). It changes nothing.
func Moved(ctx context.Context, t *Template, registry string, r ImageResolver) []string {
	var warnings []string
	for _, name := range slices.Sorted(maps.Keys(t.Lock)) {
		img, err := t.parsed(name)
		if err != nil {
			continue
		}
		locked := t.Lock[name]
		ref := registry + "/" + img.Repo + ":" + locked.Version
		switch digest, err := r.Digest(ctx, ref); {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("warning: images.%s: the moved-tag check of %s failed: %v", name, ref, err))
		case digest != locked.Digest:
			warnings = append(warnings, fmt.Sprintf("warning: images.%s: %s now resolves to %s, not the locked %s; "+
				"funcdctl app lock would take the new one", name, ref, digest, locked.Digest))
		}
	}
	return warnings
}
