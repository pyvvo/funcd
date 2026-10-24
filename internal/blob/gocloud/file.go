package gocloud

import (
	"bytes"
	"container/list"
	"context"
	"crypto/md5" //nolint:gosec // fileblob records an MD5; reads are checked against it (ADR-0278)
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
)

const (
	// fileOctetStream is the content type fileblob reports for an object without attributes (attrs.go getAttrs).
	fileOctetStream = "application/octet-stream"
	// passSetSize bounds the files whose whole-file check a ranged read remembers (ADR-0278 Decision 2).
	passSetSize = 65536
)

// fileAttrs is fileblob's .attrs JSON (attrs.go xattrs) with funcd.file, the identity of the data file the attributes
// describe, which fileblob's decoder ignores (ADR-0278 Decision 1).
type fileAttrs struct {
	CacheControl       string            `json:"user.cache_control"`
	ContentDisposition string            `json:"user.content_disposition"`
	ContentEncoding    string            `json:"user.content_encoding"`
	ContentLanguage    string            `json:"user.content_language"`
	ContentType        string            `json:"user.content_type"`
	Metadata           map[string]string `json:"user.metadata"`
	MD5                []byte            `json:"md5"`
	File               *fileIdentity     `json:"funcd.file,omitempty"`
}

// fileIdentity names one write's data file: its inode, its size and the mtime its writer set.
type fileIdentity struct {
	Ino     uint64 `json:"ino"`
	Size    int64  `json:"size"`
	MtimeNs int64  `json:"mtime_ns"`
}

// identityOf is info's identity and device; ok is false off a Unix file system.
func identityOf(info fs.FileInfo) (id fileIdentity, dev uint64, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, 0, false
	}
	// Dev is an int32 on darwin, a uint64 on linux.
	return fileIdentity{Ino: st.Ino, Size: info.Size(), MtimeNs: info.ModTime().UnixNano()}, uint64(st.Dev), true
}

// fileClock hands out the mtimes a bucket's writer sets: the wall clock in nanoseconds, above the last one, so an
// identity names one write even when the file system reuses an inode (ADR-0278 Decision 1).
type fileClock struct{ last atomic.Int64 }

func (c *fileClock) next() time.Time {
	for {
		last := c.last.Load()
		n := max(time.Now().UnixNano(), last+1)
		if c.last.CompareAndSwap(last, n) {
			return time.Unix(0, n)
		}
	}
}

// newFileAttrs is what gocloud's writer records for data (blob.go:1162-1230): metadata keys lowercased with no empty,
// duplicate or non-UTF-8 one, the content type re-formatted or detected, and the MD5. checkKey refuses a non-UTF-8 key.
func newFileAttrs(data []byte, opts blob.PutOptions) (*fileAttrs, error) {
	xa := &fileAttrs{ContentType: http.DetectContentType(data)}
	if opts.ContentType != "" {
		t, p, err := mime.ParseMediaType(opts.ContentType)
		if err != nil {
			return nil, err
		}
		xa.ContentType = mime.FormatMediaType(t, p)
	}
	if len(opts.Metadata) > 0 {
		xa.Metadata = make(map[string]string, len(opts.Metadata))
		for k, v := range opts.Metadata {
			lk := strings.ToLower(k)
			switch _, dup := xa.Metadata[lk]; {
			case k == "":
				return nil, errors.New("a metadata key is empty")
			case !utf8.ValidString(k) || !utf8.ValidString(v):
				return nil, fmt.Errorf("metadata %q is not valid UTF-8", k)
			case dup:
				return nil, fmt.Errorf("metadata key %q is given twice", lk)
			}
			xa.Metadata[lk] = v
		}
	}
	sum := md5.Sum(data) //nolint:gosec // the digest fileblob's format records
	xa.MD5 = sum[:]
	return xa, nil
}

// encode is xa as fileblob's encoder writes it (attrs.go setAttrs).
func (xa *fileAttrs) encode() ([]byte, error) {
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(xa); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// filePath is key's data file in the file:// bucket rooted at dir: fileblob's stored path.
func filePath(dir, key string) string {
	return filepath.Join(dir, filepath.FromSlash(fileEscapeKey(key)))
}

// fileWrite stores a file:// object so that it survives a power loss once it returns (ADR-0278 Decision 1): the data
// and, unless create-if-absent, its attributes are written beside the key and synced, the data is renamed (or linked)
// onto the key, then the attributes, and the directory is synced.
func (k *bucket) fileWrite(ctx context.Context, op, key string, data []byte, opts blob.PutOptions) error {
	var xa *fileAttrs
	perm := os.FileMode(0o666)
	if opts.IfNotExist {
		if opts.ContentType != "" || len(opts.Metadata) > 0 {
			return fault.Invalidf(op, "%q: a create-if-absent put on the file backend stores no content type or metadata", key)
		}
		if fileWalkPrefix(key) != key {
			return fault.Invalidf(op, "%q: the file backend stores this key under another path", key)
		}
		perm = 0o600
	} else {
		var err error
		if xa, err = newFileAttrs(data, opts); err != nil {
			return fault.Wrapf(err, fault.Invalid, op, "put %q", key)
		}
	}
	path := filePath(k.dir, key)
	if !strings.HasPrefix(path+string(os.PathSeparator), k.dir+string(os.PathSeparator)) {
		return fault.Invalidf(op, "%q escapes the bucket root", key)
	}
	dir := filepath.Dir(path)
	if err := k.disk.MkdirAll(dir, k.dirMode); err != nil {
		return mapErr(op, key, err)
	}
	tmp, id, err := k.fileTemp(path, data, perm, k.clock.next())
	if err != nil {
		return mapErr(op, key, err)
	}
	defer func() { _ = os.Remove(tmp) }()
	var attrsTmp string
	if xa != nil {
		xa.File = &id
		enc, err := xa.encode()
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "encode the attributes of %q", key)
		}
		if attrsTmp, _, err = k.fileTemp(path+fileAttrsSuffix, enc, 0o666, time.Time{}); err != nil {
			return mapErr(op, key, err)
		}
		defer func() { _ = os.Remove(attrsTmp) }()
	}
	if err := ctx.Err(); err != nil {
		return mapErr(op, key, err)
	}
	if xa == nil {
		if err := k.disk.Link(tmp, path); err != nil {
			if info, serr := os.Lstat(path); errors.Is(err, fs.ErrExist) && serr == nil && !info.IsDir() {
				return fault.Wrapf(err, fault.Conflict, op, "%q already exists", key)
			}
			return mapErr(op, key, err)
		}
	} else {
		if err := k.disk.Rename(tmp, path); err != nil {
			return mapErr(op, key, err)
		}
		if err := k.disk.Rename(attrsTmp, path+fileAttrsSuffix); err != nil {
			return mapErr(op, key, err)
		}
	}
	if err := k.disk.SyncDir(dir); err != nil {
		return mapErr(op, key, err)
	}
	return nil
}

// fileTemp writes data to a new temp file beside path at perm, sets its mtime unless zero, and syncs it. It returns the
// temp file's name and the identity fstat reads back.
func (k *bucket) fileTemp(path string, data []byte, perm os.FileMode, mtime time.Time) (string, fileIdentity, error) {
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	tmp := path + "." + hex.EncodeToString(suffix) + fileTempSuffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) //nolint:gosec // the path is the bucket's own
	if err != nil {
		return "", fileIdentity{}, err
	}
	_, err = f.Write(data)
	if err == nil && !mtime.IsZero() {
		err = os.Chtimes(tmp, mtime, mtime)
	}
	if err == nil {
		err = k.disk.SyncFile(f)
	}
	var id fileIdentity
	if err == nil {
		var info fs.FileInfo
		if info, err = f.Stat(); err == nil {
			id, _, _ = identityOf(info)
		}
	}
	if err = errors.Join(err, f.Close()); err != nil {
		_ = os.Remove(tmp)
		return "", fileIdentity{}, err
	}
	return tmp, id, nil
}

// pairFileAttrs pairs raw, key's attributes as read (rerr the read's error), with info, a stat of one of key's data
// files (ADR-0278 Decision 2): attributes naming that file, or no file, carry their MD5; attributes naming another
// file (other) or missing carry none; attributes that do not decode are blob.ErrCorrupt.
func pairFileAttrs(key string, info fs.FileInfo, raw []byte, rerr error) (a blob.Attributes, other bool, err error) {
	a = blob.Attributes{Key: key, Size: info.Size(), ModTime: info.ModTime(), ContentType: fileOctetStream}
	switch {
	case errors.Is(rerr, fs.ErrNotExist):
		return a, false, nil
	case rerr != nil:
		return blob.Attributes{}, false, rerr
	}
	var xa fileAttrs
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&xa); err != nil {
		return blob.Attributes{}, false, fmt.Errorf("%w: the attributes do not decode: %w", blob.ErrCorrupt, err)
	}
	a.ContentType, a.Metadata = xa.ContentType, xa.Metadata
	if xa.File != nil {
		if id, _, ok := identityOf(info); !ok || id != *xa.File {
			return a, true, nil
		}
	}
	a.MD5 = xa.MD5
	return a, false, nil
}

// FilePair pairs key's attributes in the file:// bucket rooted at dir with info, a stat of a data file of key (ADR-0278
// Decision 2): a.MD5 is set when the bytes must match it, other reports attributes naming another file, and attributes
// that do not decode are blob.ErrCorrupt.
func FilePair(dir, key string, info fs.FileInfo) (a blob.Attributes, other bool, err error) {
	raw, rerr := os.ReadFile(filePath(dir, key) + fileAttrsSuffix) //nolint:gosec // the bucket's own attributes
	a, other, err = pairFileAttrs(key, info, raw, rerr)
	if err != nil {
		return blob.Attributes{}, false, fault.Wrapf(err, fault.Internal, "gocloud.FilePair", "read the attributes of %q", key)
	}
	return a, other, nil
}

// fileObject is an opened file:// object: its data file's descriptor and stat, and its paired attributes.
type fileObject struct {
	f     *os.File
	info  fs.FileInfo
	attrs blob.Attributes
}

// fileOpen reads key's attributes, then opens its data file and pairs them with it, as the design model's reader does.
func (k *bucket) fileOpen(op, key string) (*fileObject, error) {
	path := filePath(k.dir, key)
	raw, rerr := os.ReadFile(path + fileAttrsSuffix) //nolint:gosec // the bucket's own attributes
	f, err := os.Open(path)                          //nolint:gosec // the bucket's own data file
	if err != nil {
		return nil, fileErr(op, key, err)
	}
	info, err := f.Stat()
	if err == nil && info.IsDir() {
		err = fs.ErrNotExist
	}
	var a blob.Attributes
	if err == nil {
		a, _, err = pairFileAttrs(key, info, raw, rerr)
	}
	if err != nil {
		_ = f.Close()
		return nil, fileErr(op, key, err)
	}
	return &fileObject{f: f, info: info, attrs: a}, nil
}

// fileErr maps a file:// read error: a missing file is fault.NotFound, attributes that do not decode or bytes that
// do not match them are blob.ErrCorrupt in fault.Internal.
func fileErr(op, key string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fault.Wrapf(err, fault.NotFound, op, "%q not found", key)
	case errors.Is(err, blob.ErrCorrupt):
		return fault.Wrapf(err, fault.Internal, op, "%q is refused", key)
	}
	return mapErr(op, key, err)
}

// fileGet reads a whole file:// object, checked against its attributes (ADR-0278 Decision 2).
func (k *bucket) fileGet(op, key string) ([]byte, error) {
	o, err := k.fileOpen(op, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = o.f.Close() }()
	data, err := io.ReadAll(o.f)
	if err != nil {
		return nil, mapErr(op, key, err)
	}
	if len(o.attrs.MD5) > 0 {
		if sum := md5.Sum(data); !bytes.Equal(sum[:], o.attrs.MD5) { //nolint:gosec // compared with the recorded MD5
			return nil, fileErr(op, key, fmt.Errorf("%w: its bytes do not match its MD5", blob.ErrCorrupt))
		}
		if pk, ok := o.passKey(); ok {
			k.passes.pass(pk)
		}
	}
	return data, nil
}

// fileGetRange reads bytes [offset, offset+length) of a file:// object, length < 0 to its end, once its whole file
// passed the check in this daemon (ADR-0278 Decision 2).
func (k *bucket) fileGetRange(op, key string, offset, length int64) ([]byte, error) {
	o, err := k.fileOpen(op, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = o.f.Close() }()
	if len(o.attrs.MD5) > 0 {
		hash := func() error {
			h := md5.New() //nolint:gosec // compared with the recorded MD5
			if _, err := io.Copy(h, io.NewSectionReader(o.f, 0, o.info.Size())); err != nil {
				return err
			}
			if !bytes.Equal(h.Sum(nil), o.attrs.MD5) {
				return fmt.Errorf("%w: its bytes do not match its MD5", blob.ErrCorrupt)
			}
			return nil
		}
		if pk, ok := o.passKey(); ok {
			err = k.passes.check(pk, hash)
		} else {
			err = hash()
		}
		if err != nil {
			return nil, fileErr(op, key, err)
		}
	}
	n := max(o.info.Size()-offset, 0)
	if length >= 0 {
		n = min(n, length)
	}
	data := make([]byte, n)
	read, err := o.f.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, mapErr(op, key, err)
	}
	return data[:read], nil
}

// fileAttributes is key's attributes paired with a stat of its data file; it hashes nothing.
func (k *bucket) fileAttributes(op, key string) (blob.Attributes, error) {
	path := filePath(k.dir, key)
	raw, rerr := os.ReadFile(path + fileAttrsSuffix) //nolint:gosec // the bucket's own attributes
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		err = fs.ErrNotExist
	}
	var a blob.Attributes
	if err == nil {
		a, _, err = pairFileAttrs(key, info, raw, rerr)
	}
	if err != nil {
		return blob.Attributes{}, fileErr(op, key, err)
	}
	return a, nil
}

// passKey is o's entry in the pass set: its file's device and identity, and the MD5 it passed against.
func (o *fileObject) passKey() (passKey, bool) {
	id, dev, ok := identityOf(o.info)
	if !ok || len(o.attrs.MD5) != md5.Size {
		return passKey{}, false
	}
	pk := passKey{dev: dev, id: id}
	copy(pk.md5[:], o.attrs.MD5)
	return pk, true
}

type passKey struct {
	dev uint64
	id  fileIdentity
	md5 [md5.Size]byte
}

// passSet remembers the files whose whole-file check passed in this daemon, at most passSetSize, the least recently
// used out first; concurrent first checks of one file share one hash, and a failure is not remembered.
type passSet struct {
	mu      sync.Mutex
	order   *list.List // of passKey, the most recently used first
	entries map[passKey]*list.Element
	flight  map[passKey]*passCall
}

type passCall struct {
	done chan struct{}
	err  error
}

func newPassSet() *passSet {
	return &passSet{order: list.New(), entries: map[passKey]*list.Element{}, flight: map[passKey]*passCall{}}
}

// check runs hash unless pk passed already or another check of pk runs, whose result it then shares.
func (p *passSet) check(pk passKey, hash func() error) error {
	p.mu.Lock()
	if e, ok := p.entries[pk]; ok {
		p.order.MoveToFront(e)
		p.mu.Unlock()
		return nil
	}
	if c, ok := p.flight[pk]; ok {
		p.mu.Unlock()
		<-c.done
		return c.err
	}
	c := &passCall{done: make(chan struct{})}
	p.flight[pk] = c
	p.mu.Unlock()
	c.err = hash()
	p.mu.Lock()
	delete(p.flight, pk)
	if c.err == nil {
		p.add(pk)
	}
	p.mu.Unlock()
	close(c.done)
	return c.err
}

// pass records pk as passed.
func (p *passSet) pass(pk passKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.add(pk)
}

func (p *passSet) add(pk passKey) {
	if e, ok := p.entries[pk]; ok {
		p.order.MoveToFront(e)
		return
	}
	p.entries[pk] = p.order.PushFront(pk)
	if p.order.Len() > passSetSize {
		oldest := p.order.Back()
		p.order.Remove(oldest)
		delete(p.entries, oldest.Value.(passKey))
	}
}
