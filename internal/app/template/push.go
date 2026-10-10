package template

import (
	"github.com/Masterminds/semver/v3"

	"github.com/pyvvo/funcd/api/fault"
)

const pushOp = "app.push"

// CheckPush loads the template a push publishes and refuses it without a fresh lock or with a version that holds
// build metadata, which an OCI tag cannot (ADR-0218 Decision 6).
func CheckPush(dir string) (*Template, error) {
	t, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if t.Lock == nil {
		return nil, fault.Invalidf(pushOp, "%s has no %s: run funcdctl app lock before a push", dir, lockFile)
	}
	if err := CheckLock(t); err != nil {
		return nil, err
	}
	if v, err := semver.StrictNewVersion(t.Version); err != nil || v.Metadata() != "" {
		return nil, fault.Invalidf(pushOp, "version %s holds build metadata: the version is the tag, and an OCI tag cannot hold a +", t.Version)
	}
	return t, nil
}
