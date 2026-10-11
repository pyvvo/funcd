// Package s3stub is an S3 endpoint for tests (ADR-0203, ADR-0208): path-style object PUT, GET, HEAD, DELETE and
// CopyObject, ListObjectsV2 and ListObjectVersions over an in-memory bucket stamped by a manual clock, with the
// bucket's versioning and Object Lock answers set by the test.
package s3stub

import (
	"bytes"
	"crypto/md5" //nolint:gosec // S3's ETag of a single-part object
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/clock"
)

// Bucket is the stub's bucket name.
const Bucket = "funcd-backup"

// Drop as a Status closes the connection without an answer.
const Drop = -1

// Condition is how the stub answers a put with If-None-Match: *.
type Condition int

const (
	Honored Condition = iota
	Ignored
	Refused
)

// Stub is the endpoint. Set its fields through Set. Box refuses every object read and delete and every put under
// gen/verified/, as ADR-0203's box policy does; FailPut refuses chosen puts and copies; OnPut runs after each stored
// put, under the stub's lock (use PutLocked there). Versioning is the bucket's status ("", "Enabled",
// "Suspended"); VersioningStatus and LockStatus, when set, answer that read with the HTTP status (or Drop).
type Stub struct {
	Clock *clock.Manual
	srv   *httptest.Server

	mu               sync.Mutex
	Cond             Condition
	Box              bool
	FailPut          func(key string) bool
	OnPut            func(s *Stub, key string)
	Versioning       string
	VersioningStatus int
	ObjectLock       bool
	LockStatus       int
	objects          map[string][]version
	nextID           int
	puts             []string
	requests         []string
	auth             []string
}

// version is one version of a key; the last of a key's list is current, absent when it is a delete marker.
type version struct {
	id          string
	data        []byte
	size        int64
	mod         time.Time
	contentType string
	meta        map[string]string
	marker      bool
}

// Main keeps the AWS SDK off the developer's shared config and credentials and the EC2 metadata service for the
// process, so tests using the stub need no t.Setenv and can run in parallel: call it from TestMain.
func Main(m *testing.M) {
	dir, err := os.MkdirTemp("", "funcd")
	if err != nil {
		fmt.Fprintln(os.Stderr, "s3stub.Main:", err) //nolint:forbidigo // TestMain has no testing.T
		os.Exit(2)
	}
	none := filepath.Join(dir, "none")
	for k, v := range map[string]string{
		"AWS_CONFIG_FILE":             none,
		"AWS_SHARED_CREDENTIALS_FILE": none,
		"AWS_EC2_METADATA_DISABLED":   "true",
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "s3stub.Main:", err) //nolint:forbidigo // TestMain has no testing.T
			os.Exit(2)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// New starts a stub stamped by c; a nil c is a manual clock at the current time.
func New(t testing.TB, c *clock.Manual) *Stub {
	t.Helper()
	if c == nil {
		c = clock.NewManual(time.Now().UTC().Truncate(time.Second))
	}
	s := &Stub{Clock: c, objects: map[string][]version{}}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the stub's bucket URL; params are added to its query (prefix=p/ for one).
func (s *Stub) URL(params ...string) string {
	u := "s3://" + Bucket + "?region=us-east-1&use_path_style=true&endpoint=" + s.srv.URL
	for _, p := range params {
		u += "&" + p
	}
	return u
}

// Endpoint is the stub's HTTP endpoint.
func (s *Stub) Endpoint() string { return s.srv.URL }

// Set changes the stub under its lock.
func (s *Stub) Set(f func(s *Stub)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// Seed stores an object as another writer would, unrecorded in the puts.
func (s *Stub) Seed(key string, data []byte) {
	s.Set(func(s *Stub) { s.PutLocked(key, data) })
}

// PutLocked stores an object; call it under the stub's lock (from OnPut or Set).
func (s *Stub) PutLocked(key string, data []byte) {
	s.store(key, version{data: data, size: int64(len(data)), mod: s.Clock.Now()})
}

// SetSize makes key's current version list with size bytes, for a test of a size limit.
func (s *Stub) SetSize(key string, size int64) {
	s.Set(func(s *Stub) {
		if vs := s.objects[key]; len(vs) > 0 {
			vs[len(vs)-1].size = size
		}
	})
}

// ExpireNoncurrentLocked drops every version of key but the current one, as a noncurrent-version expiry rule does;
// call it under the stub's lock.
func (s *Stub) ExpireNoncurrentLocked(key string) {
	if vs := s.objects[key]; len(vs) > 1 {
		s.objects[key] = vs[len(vs)-1:]
	}
}

// Keys returns the current keys under prefix, sorted.
func (s *Stub) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, k := range slices.Sorted(maps.Keys(s.objects)) {
		if _, ok := s.current(k); ok && strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// Object returns key's current bytes, content type and metadata.
func (s *Stub) Object(key string) (data []byte, contentType string, meta map[string]string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.current(key)
	return slices.Clone(v.data), v.contentType, maps.Clone(v.meta), ok
}

// VersionCount returns how many versions and delete markers key has.
func (s *Stub) VersionCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects[key])
}

// PutKeys returns the stored puts under prefix, in order.
func (s *Stub) PutKeys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, k := range s.puts {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// Seen returns every request ("<method> <key>", a sub-resource as "?<name>") and its Authorization header.
func (s *Stub) Seen() (requests, auth []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests), slices.Clone(s.auth)
}

func (s *Stub) current(key string) (version, bool) {
	vs := s.objects[key]
	if len(vs) == 0 || vs[len(vs)-1].marker {
		return version{}, false
	}
	return vs[len(vs)-1], true
}

// store adds v as key's current version: a new one while versioning is Enabled, else in place of the null one.
func (s *Stub) store(key string, v version) {
	if s.Versioning == "Enabled" {
		s.nextID++
		v.id = "v" + strconv.Itoa(s.nextID)
		s.objects[key] = append(s.objects[key], v)
		return
	}
	v.id = "null"
	vs := slices.DeleteFunc(s.objects[key], func(o version) bool { return o.id == "null" })
	s.objects[key] = append(vs, v)
}

func (s *Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"+Bucket), "/")
	q := r.URL.Query()
	sub := ""
	for _, name := range []string{"versioning", "object-lock", "versions"} {
		if q.Has(name) {
			sub = "?" + name
		}
	}
	s.requests = append(s.requests, r.Method+" "+key+sub)
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	switch {
	case sub == "?versioning":
		s.answerVersioning(w)
	case sub == "?object-lock":
		s.answerLock(w)
	case sub == "?versions":
		s.listVersions(w, q.Get("prefix"))
	case r.Method == http.MethodGet && key == "":
		s.list(w, q)
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		s.copy(w, key, r.Header.Get("X-Amz-Copy-Source"))
	case r.Method == http.MethodPut:
		s.put(w, r, key, body)
	case s.Box:
		s3Error(w, http.StatusForbidden, "AccessDenied")
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		s.get(w, r, key)
	case r.Method == http.MethodDelete:
		s.remove(key)
		w.WriteHeader(http.StatusNoContent)
	default:
		s3Error(w, http.StatusBadRequest, "BadRequest")
	}
}

func (s *Stub) answerVersioning(w http.ResponseWriter) {
	if answered(w, s.VersioningStatus) {
		return
	}
	status := ""
	if s.Versioning != "" {
		status = "<Status>" + s.Versioning + "</Status>"
	}
	writeXML(w, `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+status+`</VersioningConfiguration>`)
}

func (s *Stub) answerLock(w http.ResponseWriter) {
	if answered(w, s.LockStatus) {
		return
	}
	if !s.ObjectLock {
		s3Error(w, http.StatusNotFound, "ObjectLockConfigurationNotFoundError")
		return
	}
	writeXML(w, `<ObjectLockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
		`<ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`)
}

// answered writes the error status set for a configuration read, or drops the connection; false when none is set.
func answered(w http.ResponseWriter, status int) bool {
	switch status {
	case 0:
		return false
	case Drop:
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return true
			}
		}
		s3Error(w, http.StatusServiceUnavailable, "ServiceUnavailable")
	default:
		code := map[int]string{
			http.StatusForbidden: "AccessDenied", http.StatusNotImplemented: "NotImplemented",
			http.StatusNotFound: "NoSuchBucket",
		}[status]
		s3Error(w, status, cmpOr(code, "InternalError"))
	}
	return true
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Stub) get(w http.ResponseWriter, r *http.Request, key string) {
	v, ok := s.current(key)
	if !ok {
		s3Error(w, http.StatusNotFound, "NoSuchKey")
		return
	}
	h := w.Header()
	h.Set("Last-Modified", v.mod.UTC().Format(http.TimeFormat))
	h.Set("ETag", etag(v.data))
	h.Set("x-amz-version-id", v.id)
	if v.contentType != "" {
		h.Set("Content-Type", v.contentType)
	}
	for k, val := range v.meta {
		h.Set("x-amz-meta-"+k, val)
	}
	data, status := v.data, http.StatusOK
	if from, to, ok := byteRange(r.Header.Get("Range"), int64(len(data))); ok {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to-1, len(data)))
		data, status = data[from:to], http.StatusPartialContent
	}
	if status == http.StatusOK {
		h.Set("x-amz-checksum-crc32", base64.StdEncoding.EncodeToString(binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(data))))
	}
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

// byteRange parses "bytes=a-b" or "bytes=a-" within size.
func byteRange(h string, size int64) (from, to int64, ok bool) {
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found {
		return 0, 0, false
	}
	a, b, _ := strings.Cut(spec, "-")
	from, err := strconv.ParseInt(a, 10, 64)
	if err != nil || from > size {
		return 0, 0, false
	}
	to = size
	if b != "" {
		if end, err := strconv.ParseInt(b, 10, 64); err == nil && end+1 < size {
			to = end + 1
		}
	}
	return from, to, true
}

// remove deletes key: a delete marker while versioning is Enabled, else the null version.
func (s *Stub) remove(key string) {
	if s.Versioning == "Enabled" {
		if len(s.objects[key]) > 0 {
			s.nextID++
			s.objects[key] = append(s.objects[key], version{id: "v" + strconv.Itoa(s.nextID), mod: s.Clock.Now(), marker: true})
		}
		return
	}
	vs := slices.DeleteFunc(s.objects[key], func(o version) bool { return o.id == "null" })
	if len(vs) == 0 {
		delete(s.objects, key)
		return
	}
	s.objects[key] = vs
}

func (s *Stub) put(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") ||
		strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		body = decodeChunked(body)
	}
	conditional := r.Header.Get("If-None-Match") == "*"
	_, exists := s.current(key)
	switch {
	case s.FailPut != nil && s.FailPut(key), s.Box && strings.HasPrefix(key, "gen/verified/"):
		s3Error(w, http.StatusForbidden, "AccessDenied")
		return
	case conditional && s.Cond == Refused:
		s3Error(w, http.StatusNotImplemented, "NotImplemented")
		return
	case conditional && s.Cond == Honored && exists:
		s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
		return
	}
	meta := map[string]string{}
	for k, vals := range r.Header {
		if name, ok := strings.CutPrefix(strings.ToLower(k), "x-amz-meta-"); ok && len(vals) > 0 {
			meta[name] = vals[0]
		}
	}
	s.store(key, version{data: body, size: int64(len(body)), mod: s.Clock.Now(), contentType: r.Header.Get("Content-Type"), meta: meta})
	s.puts = append(s.puts, key)
	if s.OnPut != nil {
		s.OnPut(s, key)
	}
	w.Header().Set("ETag", etag(body))
}

// copy is CopyObject from "<bucket>/<key>?versionId=<id>" (URL-encoded) onto key.
func (s *Stub) copy(w http.ResponseWriter, key, source string) {
	src, id, _ := strings.Cut(source, "?versionId=")
	src, err := url.PathUnescape(strings.TrimPrefix(src, "/"))
	if err != nil || !strings.HasPrefix(src, Bucket+"/") {
		s3Error(w, http.StatusBadRequest, "InvalidArgument")
		return
	}
	if s.FailPut != nil && s.FailPut(key) {
		s3Error(w, http.StatusForbidden, "AccessDenied")
		return
	}
	srcKey := strings.TrimPrefix(src, Bucket+"/")
	i := slices.IndexFunc(s.objects[srcKey], func(v version) bool { return v.id == id && !v.marker })
	if i < 0 {
		s3Error(w, http.StatusNotFound, "NoSuchVersion")
		return
	}
	v := s.objects[srcKey][i]
	v.mod = s.Clock.Now()
	s.store(key, v)
	s.puts = append(s.puts, key)
	writeXML(w, `<CopyObjectResult><ETag>`+etag(v.data)+`</ETag><LastModified>`+v.mod.UTC().Format(time.RFC3339)+`</LastModified></CopyObjectResult>`)
}

type listResult struct {
	XMLName               xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Name                  string
	Prefix                string
	KeyCount              int
	MaxKeys               int
	IsTruncated           bool
	NextContinuationToken string `xml:",omitempty"`
	Contents              []listEntry
}

type listEntry struct {
	Key          string
	LastModified string
	ETag         string
	Size         int64
	StorageClass string
}

// list is ListObjectsV2 with max-keys, start-after and continuation-token (the last key of the page before).
func (s *Stub) list(w http.ResponseWriter, q url.Values) {
	prefix := q.Get("prefix")
	limit := 1000
	if n, err := strconv.Atoi(q.Get("max-keys")); err == nil && n > 0 {
		limit = n
	}
	after := max(q.Get("start-after"), q.Get("continuation-token"))
	res := listResult{Name: Bucket, Prefix: prefix, MaxKeys: limit}
	for _, k := range slices.Sorted(maps.Keys(s.objects)) {
		v, ok := s.current(k)
		if !ok || !strings.HasPrefix(k, prefix) || k <= after {
			continue
		}
		if len(res.Contents) == limit {
			res.IsTruncated, res.NextContinuationToken = true, res.Contents[limit-1].Key
			break
		}
		res.Contents = append(res.Contents, listEntry{Key: k, LastModified: stamp(v.mod), ETag: etag(v.data), Size: v.size, StorageClass: "STANDARD"})
	}
	res.KeyCount = len(res.Contents)
	encode(w, res)
}

type versionsResult struct {
	XMLName      xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListVersionsResult"`
	Name         string
	Prefix       string
	MaxKeys      int
	IsTruncated  bool
	Version      []versionEntry
	DeleteMarker []markerEntry
}

type versionEntry struct {
	Key          string
	VersionId    string //nolint:revive // the S3 element name
	IsLatest     bool
	LastModified string
	ETag         string
	Size         int64
	StorageClass string
}

type markerEntry struct {
	Key          string
	VersionId    string //nolint:revive // the S3 element name
	IsLatest     bool
	LastModified string
}

// listVersions is ListObjectVersions in one page, each key's versions newest first.
func (s *Stub) listVersions(w http.ResponseWriter, prefix string) {
	res := versionsResult{Name: Bucket, Prefix: prefix, MaxKeys: 1000}
	for _, k := range slices.Sorted(maps.Keys(s.objects)) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		vs := s.objects[k]
		for i := len(vs) - 1; i >= 0; i-- {
			v, latest := vs[i], i == len(vs)-1
			if v.marker {
				res.DeleteMarker = append(res.DeleteMarker, markerEntry{Key: k, VersionId: v.id, IsLatest: latest, LastModified: stamp(v.mod)})
				continue
			}
			res.Version = append(res.Version, versionEntry{Key: k, VersionId: v.id, IsLatest: latest, LastModified: stamp(v.mod),
				ETag: etag(v.data), Size: v.size, StorageClass: "STANDARD"})
		}
	}
	encode(w, res)
}

// etag is S3's ETag of a single-part object: its quoted MD5.
func etag(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // S3's ETag, not a security hash
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func encode[T listResult | versionsResult](w http.ResponseWriter, v T) {
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(v)
}

func writeXML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, body)
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "<Error><Code>"+code+"</Code><Message>"+code+"</Message></Error>")
}

// decodeChunked strips aws-chunked framing: <hex size>[;ext]\r\n<data>\r\n …, ending at a zero-size chunk.
func decodeChunked(b []byte) []byte {
	var out []byte
	for {
		line, rest, ok := bytes.Cut(b, []byte("\r\n"))
		if !ok {
			return out
		}
		size, _, _ := bytes.Cut(line, []byte(";"))
		n, err := strconv.ParseInt(string(size), 16, 64)
		if err != nil || n == 0 || int64(len(rest)) < n {
			return out
		}
		out = append(out, rest[:n]...)
		b = bytes.TrimPrefix(rest[n:], []byte("\r\n"))
	}
}

// CredentialsFile writes an AWS shared credentials file whose [default] profile holds keyID.
func CredentialsFile(t testing.TB, keyID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials")
	data := "[default]\naws_access_key_id = " + keyID + "\naws_secret_access_key = secret-" + keyID + "\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	return path
}
