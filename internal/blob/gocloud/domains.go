package gocloud

import (
	"errors"
	"io/fs"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

// Overlap is how much a backup target shares with the store it copies (ADR-0208 Decision 3).
type Overlap int

const (
	// OverlapNone is an independent target.
	OverlapNone Overlap = iota
	// OverlapProvider is the same S3 endpoint or the same device: one provider's failure takes both.
	OverlapProvider
	// OverlapStore is the store itself: the same bucket, or one directory inside the other.
	OverlapStore
)

// CompareDomains compares two bucket URLs: s3:// on the same normalized endpoint (and bucket ⇒ OverlapStore), or
// file:// directories, resolved through their symlinks, one inside the other (else the same device ⇒
// OverlapProvider). A missing directory resolves through its nearest existing ancestor; any other pair of schemes is
// OverlapNone. An unparsable URL is fault.Invalid.
func CompareDomains(store, target string) (Overlap, error) {
	const op = "gocloud.CompareDomains"
	su, err := parseURL(store)
	if err != nil {
		return OverlapNone, fault.Wrapf(err, fault.Invalid, op, "parse %q", store)
	}
	tu, err := parseURL(target)
	if err != nil {
		return OverlapNone, fault.Wrapf(err, fault.Invalid, op, "parse %q", target)
	}
	switch {
	case su.Scheme == "s3" && tu.Scheme == "s3":
		se, err := s3Endpoint(su)
		if err != nil {
			return OverlapNone, fault.Wrapf(err, fault.Invalid, op, "endpoint of %q", store)
		}
		te, err := s3Endpoint(tu)
		if err != nil {
			return OverlapNone, fault.Wrapf(err, fault.Invalid, op, "endpoint of %q", target)
		}
		switch {
		case se != te:
			return OverlapNone, nil
		case su.Host == tu.Host:
			return OverlapStore, nil
		}
		return OverlapProvider, nil
	case su.Scheme == "file" && tu.Scheme == "file":
		return compareDirs(store, target)
	}
	return OverlapNone, nil
}

func parseURL(s string) (*neturl.URL, error) {
	u, err := neturl.Parse(s)
	if err != nil {
		return nil, err
	}
	if _, err := neturl.ParseQuery(u.RawQuery); err != nil {
		return nil, err
	}
	return u, nil
}

// s3Endpoint normalizes an s3:// URL's endpoint: lowercase scheme and host, no default port or trailing "/"; none
// is AWS in the URL's region.
func s3Endpoint(u *neturl.URL) (string, error) {
	q := u.Query()
	ep := q.Get("endpoint")
	if ep == "" {
		return "aws:" + strings.ToLower(q.Get("region")), nil
	}
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	e, err := neturl.Parse(ep)
	if err != nil {
		return "", err
	}
	scheme, host := strings.ToLower(e.Scheme), strings.ToLower(e.Host)
	if p := e.Port(); scheme == "https" && p == "443" || scheme == "http" && p == "80" {
		host = strings.ToLower(e.Hostname())
	}
	return scheme + "://" + host + strings.TrimRight(e.Path, "/"), nil
}

func compareDirs(store, target string) (Overlap, error) {
	const op = "gocloud.CompareDomains"
	sd, sdev, err := resolveDir(store)
	if err != nil {
		return OverlapNone, fault.Wrapf(err, fault.Invalid, op, "resolve %q", store)
	}
	td, tdev, err := resolveDir(target)
	if err != nil {
		return OverlapNone, fault.Wrapf(err, fault.Invalid, op, "resolve %q", target)
	}
	switch {
	case within(sd, td) || within(td, sd):
		return OverlapStore, nil
	case sdev == tdev:
		return OverlapProvider, nil
	}
	return OverlapNone, nil
}

// resolveDir is a file:// URL's directory with its nearest existing ancestor's symlinks resolved, and that
// ancestor's device.
func resolveDir(url string) (string, uint64, error) {
	dir, _, err := fileDir(url)
	if err != nil {
		return "", 0, err
	}
	existing, rest := dir, ""
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			var st unix.Stat_t
			if err := unix.Stat(resolved, &st); err != nil {
				return "", 0, err
			}
			return filepath.Join(resolved, rest), uint64(st.Dev), nil //nolint:unconvert,gosec // Dev is int32 on darwin
		}
		parent := filepath.Dir(existing)
		if !errors.Is(err, fs.ErrNotExist) || parent == existing {
			return "", 0, err
		}
		existing, rest = parent, filepath.Join(filepath.Base(existing), rest)
	}
}

// within reports dir at or below root.
func within(dir, root string) bool {
	return dir == root || strings.HasPrefix(dir, root+string(os.PathSeparator)) || root == string(os.PathSeparator)
}
