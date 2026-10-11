package blobmirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
)

// Restore puts generation n of src into the empty dst (ADR-0208 Decision 6): it calls open once with the manifest's
// recipients and reads the index through it; an object is read through it too, or as is when the manifest names no
// recipient (user bytes, never sniffed). A missing part or sha256- key is fault.NotFound and a non-empty dst
// fault.Conflict, both before any write; an object whose stored bytes miss their SHA-256 is fault.Invalid. An error
// after a write deletes what it wrote.
func Restore(ctx context.Context, src blob.Bucket, generation uint64, dst blob.Bucket, open backup.Opener) (_ Manifest, err error) {
	const op = "blobmirror.Restore"
	l, err := list(ctx, src)
	if err != nil {
		return Manifest{}, err
	}
	g := l.gens[generation]
	if g == nil || !g.complete {
		return Manifest{}, fault.NotFoundf(op, "blob generation %d has no manifest", generation)
	}
	data, err := src.Get(ctx, genDir(g.epoch, generation)+manifestName)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fault.Wrapf(err, fault.Invalid, op, "decode the manifest of blob generation %d", generation)
	}
	if m.Format > Format {
		return Manifest{}, fault.Invalidf(op, "blob generation %d has format %d; this funcd reads format %d", generation, m.Format, Format)
	}
	unseal, err := open(m.Recipients)
	if err != nil {
		return Manifest{}, err
	}
	objUnseal := unseal
	if len(m.Recipients) == 0 {
		objUnseal = nil
	}
	objects, err := readIndex(ctx, src, g, m, unseal)
	if err != nil {
		return Manifest{}, err
	}
	for _, o := range objects {
		if !l.uploads[m.Epoch][o.ID][o.Gen].complete(o.Parts, o.SHA256) {
			return Manifest{}, fault.NotFoundf(op, "object %q of blob generation %d: a part or its %s key is missing under %s",
				o.Key, generation, shaPrefix, objectDir(m.Epoch, o.ID, o.Gen))
		}
	}
	items, _, err := dst.ListAfter(ctx, "", "", 1)
	if err != nil {
		return Manifest{}, err
	}
	if len(items) > 0 {
		return Manifest{}, fault.Conflictf(op, "the destination store holds %q: a restore writes into an empty store", items[0].Key)
	}
	var written []string
	defer func() {
		if err != nil {
			for _, k := range written {
				err = errors.Join(err, dst.Delete(context.WithoutCancel(ctx), k))
			}
		}
	}()
	for _, o := range objects {
		stored, err := readParts(ctx, src, objectDir(m.Epoch, o.ID, o.Gen), o.Parts)
		if err != nil {
			return Manifest{}, err
		}
		if sum := sha256.Sum256(stored); hex.EncodeToString(sum[:]) != o.SHA256 {
			return Manifest{}, fault.Invalidf(op, "object %q of blob generation %d does not match its SHA-256", o.Key, generation)
		}
		plain, err := unsealAll(stored, objUnseal)
		if err != nil {
			return Manifest{}, fault.Wrapf(err, fault.KindOf(err), op, "open object %q", o.Key)
		}
		if err := dst.Put(ctx, o.Key, plain, blob.PutOptions{ContentType: o.ContentType, Metadata: o.Metadata}); err != nil {
			return Manifest{}, err
		}
		written = append(written, o.Key)
	}
	return m, nil
}

// readIndex reads, checks and decodes a generation's index.
func readIndex(ctx context.Context, src blob.Bucket, g *generation, m Manifest, unseal backup.Unseal) ([]Object, error) {
	const op = "blobmirror.Restore"
	for i := range m.Index.Parts {
		if !g.index[i] {
			return nil, fault.NotFoundf(op, "blob generation %d: index part %d is missing", m.Generation, i)
		}
	}
	stored, err := readParts(ctx, src, genDir(m.Epoch, m.Generation)+"index/", m.Index.Parts)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(stored); hex.EncodeToString(sum[:]) != m.Index.SHA256 || int64(len(stored)) != m.Index.Bytes {
		return nil, fault.Invalidf(op, "the index of blob generation %d does not match its manifest", m.Generation)
	}
	plain, err := unsealAll(stored, unseal)
	if err != nil {
		return nil, err
	}
	var out []Object
	dec := json.NewDecoder(bytes.NewReader(plain))
	for {
		var o Object
		err := dec.Decode(&o)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "decode the index of blob generation %d", m.Generation)
		}
		out = append(out, o)
	}
	if len(out) != m.Objects {
		return nil, fault.Invalidf(op, "the index of blob generation %d lists %d objects, its manifest %d", m.Generation, len(out), m.Objects)
	}
	return out, nil
}

func readParts(ctx context.Context, src blob.Bucket, dir string, parts int) ([]byte, error) {
	var out []byte
	for i := range parts {
		data, err := src.Get(ctx, fmt.Sprintf("%spart-%05d", dir, i))
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	return out, nil
}

// unsealAll opens stored through unseal, nil ⇒ as is.
func unsealAll(stored []byte, unseal backup.Unseal) ([]byte, error) {
	if unseal == nil {
		return stored, nil
	}
	r, err := unseal(bytes.NewReader(stored))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}
