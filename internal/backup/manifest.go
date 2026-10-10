package backup

import (
	"context"

	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// Manifest is a generation's manifest.yaml (Decision 3), written last: the generation exists once it does. A new
// field's absence means the behavior before it; a changed meaning, framing or layout bumps Format. Readers ignore
// unknown fields.
type Manifest struct {
	Format     int    `json:"format"`
	Generation uint64 `json:"generation"`
	// At is when the cut began (ADR-0196's form).
	At    v1alpha1.Timestamp `json:"at"`
	Funcd string             `json:"funcd"`
	// Timeline and Revision split the metastore version the cut returned.
	Timeline string `json:"timeline"`
	Revision uint64 `json:"revision"`
	// Parent is the generation a restore loaded; nil when the timeline began at a first start.
	Parent *GenRef     `json:"parent,omitempty"`
	Stores []StoreFile `json:"stores"`
	Keys
}

// Keys are the envelope's key records, whose values ADR-0204 defines; absent ⇒ none recorded, and no Recipients ⇒
// the stores are unsealed.
type Keys struct {
	SecretsKey   string   `json:"secretsKey,omitempty"`
	MasterSecret string   `json:"masterSecret,omitempty"`
	Recipients   []string `json:"recipients,omitempty"`
}

// GenRef names a generation: expiry can return a number, so n alone does not.
type GenRef struct {
	Timeline   string `json:"timeline"`
	Generation uint64 `json:"generation"`
}

// StoreFile is one store's file, in cut order: its parts, and the size and SHA-256 (hex) of the stored bytes.
type StoreFile struct {
	Name   string `json:"name"`
	Parts  int    `json:"parts"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// ReadManifest reads the manifest of e; it needs a credential that reads.
func ReadManifest(ctx context.Context, b blob.Bucket, e Entry) (Manifest, error) {
	key := GenDir(e.Class, e.Generation, e.Timeline) + manifestName
	data, err := b.Get(ctx, key)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fault.Wrapf(err, fault.Invalid, "backup.ReadManifest", "decode %s", key)
	}
	return m, nil
}

// Abandoned returns the generations on an abandoned branch: a parent timeline's generations numbered above the
// generation a restore loaded from it.
func Abandoned(ms []Manifest) map[GenRef]bool {
	out := map[GenRef]bool{}
	for _, m := range ms {
		if m.Parent == nil {
			continue
		}
		for _, o := range ms {
			if o.Timeline == m.Parent.Timeline && o.Generation > m.Parent.Generation {
				out[GenRef{Timeline: o.Timeline, Generation: o.Generation}] = true
			}
		}
	}
	return out
}
