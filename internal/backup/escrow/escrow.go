// Package escrow holds the restore key rules over the operator's escrow set (ADR-0204 Decisions 4, 5): the secrets
// key and the master secret a generation names, matched by content fingerprint, and the credentials a new master
// changes. funcd never writes the escrow set.
package escrow

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/store"
)

// The escrow set's key directories (Decision 4).
const SecretsDir, MasterDir = "secrets", "master"

// The credentials a new master secret changes (Decision 5).
const (
	s3Keypair    = "s3-keypair"
	catalogToken = "catalog-token"
)

// Find returns the file under dir/sub whose envelope.Fingerprint is fp; none ⇒ fault.NotFound naming fp, the
// fingerprints found and every symlink that does not resolve. Symlinked files and directories count as regular ones.
// The bytes are fingerprinted as they are, so an escrow copy must be byte-exact.
func Find(dir, sub, fp string) (string, error) {
	const op = "escrow.Find"
	if dir == "" {
		return "", fault.NotFoundf(op, "no escrow directory given to find %s", fp)
	}
	root := filepath.Join(dir, sub)
	s := search{fp: fp, seen: map[string]bool{}}
	err := s.visit(root)
	switch {
	case s.match != "":
		return s.match, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", fault.Wrapf(err, fault.Invalid, op, "read escrow directory %s", root)
	}
	if len(s.found) == 0 {
		s.found = []string{"none"}
	}
	return "", fault.NotFoundf(op, "no file under %s has fingerprint %s; found: %s", root, fp, strings.Join(s.found, ", "))
}

// search is one Find: seen holds the resolved directories already read, so a symlink loop ends.
type search struct {
	fp, match string
	found     []string
	seen      map[string]bool
}

// visit fingerprints the file at p or reads the directory at p, through symlinks; a symlink that does not resolve is
// listed in found rather than dropped.
func (s *search) visit(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		if l, lerr := os.Lstat(p); lerr == nil && l.Mode()&fs.ModeSymlink != 0 {
			s.found = append(s.found, "unresolved symlink ("+err.Error()+")")
			return nil
		}
		return err
	}
	if info.Mode().IsRegular() {
		b, err := os.ReadFile(p) //nolint:gosec // the operator's escrow directory
		if err != nil {
			return err
		}
		if got := envelope.Fingerprint(b); got != s.fp {
			s.found = append(s.found, got+" ("+p+")")
		} else {
			s.match = p
		}
		return nil
	}
	if !info.IsDir() {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || s.seen[resolved] {
		return err
	}
	s.seen[resolved] = true
	entries, err := os.ReadDir(p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := s.visit(filepath.Join(p, e.Name())); err != nil || s.match != "" {
			return err
		}
	}
	return nil
}

// CheckSecretsKey requires the restore's secrets key to have the fingerprint m names, or both to be absent; else
// fault.Invalid naming that fingerprint and the escrow file matching it, if any. nil configured ⇒ no key.
func CheckSecretsKey(m backup.Manifest, configured []byte, dir string) error {
	const op = "escrow.CheckSecretsKey"
	got := ""
	if configured != nil {
		got = envelope.Fingerprint(configured)
	}
	switch {
	case got == m.SecretsKey:
		return nil
	case m.SecretsKey == "":
		return fault.Invalidf(op, "secrets.encryptionKeyFile is set (fingerprint %s) but generation %d names no "+
			"secrets key: unset it", got, m.Generation)
	}
	hint := "no escrow file matches it"
	if dir != "" {
		if p, err := Find(dir, SecretsDir, m.SecretsKey); err == nil {
			hint = "the escrow file " + p + " matches it"
		}
	}
	have := got
	if have == "" {
		have = "none"
	}
	return fault.Invalidf(op, "generation %d was written under secrets key %s and secrets.encryptionKeyFile has %s; "+
		"%s", m.Generation, m.SecretsKey, have, hint)
}

// MasterPlan is what a restore does with the master secret after the load: write Install, when set, to Path 0600;
// List: acceptNew took another master, or m names none, so ListChanged prints the credentials that change.
type MasterPlan struct {
	Install []byte
	Path    string
	List    bool
}

// PlanMaster, before any part is read, writes nothing (Decision 5): a set masterFile must have m's fingerprint;
// unset, the matching file under dir/MasterDir is to be installed at Decision 7's path; else fault.Invalid naming the
// fingerprint, unless acceptNew. m naming no master is no check, nothing installed, and a list of what may change.
func PlanMaster(m backup.Manifest, masterFile, dataDir, dir string, acceptNew bool) (MasterPlan, error) {
	const op = "escrow.PlanMaster"
	plan := MasterPlan{Path: s3gateway.MasterPath(masterFile, dataDir)}
	if m.MasterSecret == "" {
		plan.List = true
		return plan, nil
	}
	refuse := func(why string) (MasterPlan, error) {
		if acceptNew {
			plan.List = true
			return plan, nil
		}
		return MasterPlan{}, fault.Invalidf(op, "generation %d needs master secret %s: %s; escrow it or pass "+
			"--new-master-secret", m.Generation, m.MasterSecret, why)
	}
	if masterFile != "" {
		b, err := os.ReadFile(masterFile) //nolint:gosec // the operator's s3gateway.masterSecretFile
		if err != nil {
			return refuse("s3gateway.masterSecretFile " + masterFile + " does not read: " + err.Error())
		}
		if envelope.Fingerprint(b) != m.MasterSecret {
			return refuse("s3gateway.masterSecretFile " + masterFile + " has " + envelope.Fingerprint(b))
		}
		return plan, nil
	}
	p, err := Find(dir, MasterDir, m.MasterSecret)
	if err != nil {
		return refuse(err.Error())
	}
	b, err := os.ReadFile(p) //nolint:gosec // the operator's escrow directory
	if err != nil {
		return MasterPlan{}, fault.Wrapf(err, fault.Invalid, op, "read escrow file %s", p)
	}
	plan.Install = b
	return plan, nil
}

// Derived is one credential a new master secret changes: Credential is "s3-keypair" or "catalog-token".
type Derived struct {
	Kind            v1.Kind
	Namespace, Name string
	Credential      string
}

// ListChanged reads the loaded store for every credential derived from the master (Decision 5): a Function's
// spec.blob keypair (addS3Env) and spec.catalogs token (resolveCatalogEnv), a CatalogService's spec.blob keypair
// (engineEnv); keypairs gateway on or off, since a copy may predate the config. nil unless plan.List.
func ListChanged(ctx context.Context, plan MasterPlan, st store.Store) ([]Derived, error) {
	if !plan.List {
		return nil, nil
	}
	fns, err := st.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	css, err := st.List(ctx, v1.KindCatalogService.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Derived
	for _, o := range fns.Items {
		fn, ok := o.(*v1.Function)
		if !ok {
			continue
		}
		if len(fn.Spec.Blob) > 0 {
			out = append(out, Derived{v1.KindFunction, string(fn.Namespace), string(fn.Name), s3Keypair})
		}
		if len(fn.Spec.Catalogs) > 0 {
			out = append(out, Derived{v1.KindFunction, string(fn.Namespace), string(fn.Name), catalogToken})
		}
	}
	for _, o := range css.Items {
		if cs, ok := o.(*v1.CatalogService); ok && len(cs.Spec.Blob) > 0 {
			out = append(out, Derived{v1.KindCatalogService, string(cs.Namespace), string(cs.Name), s3Keypair})
		}
	}
	slices.SortFunc(out, func(a, b Derived) int {
		return cmp.Or(strings.Compare(string(a.Kind), string(b.Kind)), strings.Compare(a.Namespace, b.Namespace),
			strings.Compare(a.Name, b.Name), strings.Compare(a.Credential, b.Credential))
	})
	return out, nil
}
