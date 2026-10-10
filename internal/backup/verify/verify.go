// Package verify checks a platform backup generation off the box and pins it under gen/verified/ (ADR-0205 Decision
// 6): the parts against the manifest, a full decrypt with the operator's identities, the framing and key order, and
// with an escrow set the keys the generation names. The daemon holds no read credential and no identity.
package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"slices"

	"filippo.io/age"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/escrow"
	"github.com/pyvvo/funcd/internal/blob"
)

const op = "verify.Verify"

// Options configure Verify. Bucket is the target opened with the verify credential (gocloud.OpenWith); Identities
// are envelope.ReadIdentities'; EscrowDir "" leaves the keys unchecked; Generation 0 is the newest complete ladder one.
type Options struct {
	Bucket     blob.Bucket
	Identities []age.Identity
	EscrowDir  string
	Generation uint64
}

// Result is a passed verification: Pinned is false when the gen/verified/ copy already existed, so nothing was written.
type Result struct {
	Generation  uint64
	Records     int64
	KeysChecked bool
	Pinned      bool
}

// Verify checks one generation and pins it. Damage is fault.Invalid naming the store and part, and pins nothing; no
// such generation is fault.NotFound.
func Verify(ctx context.Context, o Options) (Result, error) {
	entries, err := backup.List(ctx, o.Bucket)
	if err != nil {
		return Result{}, err
	}
	e, err := pick(entries, o.Generation)
	if err != nil {
		return Result{}, err
	}
	m, err := backup.ReadManifest(ctx, o.Bucket, e)
	if err != nil {
		return Result{}, err
	}
	if m.Generation != e.Generation || m.Timeline != e.Timeline {
		return Result{}, fault.Invalidf(op, "%smanifest.yaml names generation %d of timeline %s", dir(e.Class, e),
			m.Generation, m.Timeline)
	}
	unseal, err := envelope.Opener(o.Identities)(m.Recipients)
	if err != nil {
		return Result{}, err
	}
	keys, err := o.Bucket.List(ctx, dir(e.Class, e))
	if err != nil {
		return Result{}, err
	}
	present := map[string]bool{}
	for _, a := range keys {
		present[a.Key] = true
	}
	res := Result{Generation: e.Generation}
	for _, s := range m.Stores {
		n, err := checkStore(ctx, o.Bucket, dir(e.Class, e), s, present, unseal)
		if err != nil {
			return Result{}, err
		}
		res.Records += n
	}
	if o.EscrowDir != "" {
		for _, k := range []struct{ sub, fp string }{{escrow.SecretsDir, m.SecretsKey}, {escrow.MasterDir, m.MasterSecret}} {
			if k.fp == "" {
				continue
			}
			if _, err := escrow.Find(o.EscrowDir, k.sub, k.fp); err != nil {
				return Result{}, err
			}
		}
		res.KeysChecked = true
	}
	if res.Pinned, err = pin(ctx, o.Bucket, entries, e, m); err != nil {
		return Result{}, err
	}
	return res, nil
}

// pick returns the complete generation numbered n, or for 0 the newest complete ladder one.
func pick(entries []backup.Entry, n uint64) (backup.Entry, error) {
	var found *backup.Entry
	for i, e := range entries {
		if !e.Complete || e.Class == backup.Verified {
			continue
		}
		ladder := e.Class == backup.Hourly || e.Class == backup.Daily || e.Class == backup.Weekly
		if (n == 0 && ladder) || e.Generation == n {
			found = &entries[i]
		}
	}
	if found == nil {
		if n == 0 {
			return backup.Entry{}, fault.NotFoundf(op, "the target holds no complete hourly, daily or weekly generation")
		}
		return backup.Entry{}, fault.NotFoundf(op, "the target holds no complete generation %d", n)
	}
	return *found, nil
}

// dir is a generation's directory under the class given (ADR-0203 Decision 2); a verified copy keeps its original's name.
func dir(class backup.Class, e backup.Entry) string {
	return fmt.Sprintf("gen/%s/%010d-%s/", class, e.Generation, e.Timeline)
}

func partKey(gen, store string, i int) string { return fmt.Sprintf("%s%s/part-%05d", gen, store, i) }

// checkStore reads a store file's parts in order through the unseal and the framing, keys strictly ascending, and
// matches its parts, bytes and SHA-256 with the manifest; it returns the records read.
func checkStore(ctx context.Context, b blob.Bucket, gen string, s backup.StoreFile, present map[string]bool, unseal backup.Unseal) (int64, error) {
	for i := range s.Parts {
		if !present[partKey(gen, s.Name, i)] {
			return 0, fault.Invalidf(op, "store %s: part-%05d of %d is missing", s.Name, i, s.Parts)
		}
	}
	if extra := partKey(gen, s.Name, s.Parts); present[extra] {
		return 0, fault.Invalidf(op, "store %s: part-%05d is beyond the %d parts the manifest names", s.Name, s.Parts, s.Parts)
	}
	pr := &parts{n: s.Parts, h: sha256.New(), get: func(i int) ([]byte, error) { return b.Get(ctx, partKey(gen, s.Name, i)) }}
	damaged := func(err error, what string) error {
		return fault.Wrapf(err, fault.Invalid, op, "store %s, part-%05d: %s", s.Name, pr.cur(), what)
	}
	plain, err := unseal(pr)
	if err != nil {
		return 0, damaged(err, "open the store file")
	}
	next := backup.Records(plain)
	var prev []byte
	var n int64
	for {
		rec, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, damaged(err, "read the records")
		}
		if n > 0 && bytes.Compare(rec.Key, prev) <= 0 {
			return 0, fault.Invalidf(op, "store %s, part-%05d: record %d's key does not sort after the one before", s.Name, pr.cur(), n)
		}
		prev, n = rec.Key, n+1
	}
	if _, err := io.Copy(io.Discard, pr); err != nil {
		return 0, damaged(err, "read the parts")
	}
	if sum := hex.EncodeToString(pr.h.Sum(nil)); pr.bytes != s.Bytes || sum != s.SHA256 {
		return 0, fault.Invalidf(op, "store %s, part-00000 to part-%05d: %d bytes with sha256 %s, the manifest names %d "+
			"bytes with sha256 %s", s.Name, max(s.Parts-1, 0), pr.bytes, sum, s.Bytes, s.SHA256)
	}
	return n, nil
}

// parts reads a store file's parts in order, hashing and counting the bytes.
type parts struct {
	get   func(i int) ([]byte, error)
	n, i  int
	buf   []byte
	h     hash.Hash
	bytes int64
}

// cur is the part being read.
func (p *parts) cur() int { return max(p.i-1, 0) }

func (p *parts) Read(dst []byte) (int, error) {
	for len(p.buf) == 0 {
		if p.i == p.n {
			return 0, io.EOF
		}
		data, err := p.get(p.i)
		if err != nil {
			return 0, err
		}
		p.buf, p.i = data, p.i+1
		p.h.Write(data)
		p.bytes += int64(len(data))
	}
	k := copy(dst, p.buf)
	p.buf = p.buf[k:]
	return k, nil
}

// pin copies the generation to gen/verified/<n>-<timeline>/, its parts first and the manifest last, each only where
// absent; a copy already complete writes nothing (false). A part already there must match.
func pin(ctx context.Context, b blob.Bucket, entries []backup.Entry, e backup.Entry, m backup.Manifest) (bool, error) {
	if slices.ContainsFunc(entries, func(v backup.Entry) bool {
		return v.Class == backup.Verified && v.Complete && v.Generation == e.Generation && v.Timeline == e.Timeline
	}) {
		return false, nil
	}
	src, dst := dir(e.Class, e), dir(backup.Verified, e)
	var keys []string
	for _, s := range m.Stores {
		for i := range s.Parts {
			keys = append(keys, partKey("", s.Name, i))
		}
	}
	for _, k := range append(keys, "manifest.yaml") {
		data, err := b.Get(ctx, src+k)
		if err != nil {
			return false, err
		}
		if err := putOnce(ctx, b, dst+k, data); err != nil {
			return false, err
		}
	}
	return true, nil
}

// putOnce puts data where key is absent; a present key must hold the same bytes.
func putOnce(ctx context.Context, b blob.Bucket, key string, data []byte) error {
	err := b.Put(ctx, key, data, blob.PutOptions{IfNotExist: true})
	if fault.KindOf(err) != fault.Conflict {
		return err
	}
	have, gerr := b.Get(ctx, key)
	if gerr != nil {
		return gerr
	}
	if !bytes.Equal(have, data) {
		return fault.Conflictf(op, "%s already holds other bytes than its original", key)
	}
	return nil
}
