package backup_test

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
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

const stubBucket = "funcd-backup"

// condition is how the stub answers a put with If-None-Match: *.
type condition int

const (
	conditionHonored condition = iota
	conditionIgnored
	conditionRefused
)

// s3stub is an S3 endpoint for the backup tests: path-style PUT, GET, HEAD, DELETE and ListObjectsV2 over an
// in-memory map, stamped by a manual clock. box refuses every read, delete and put under gen/verified/, as the box
// policy does; failPut refuses chosen puts; onPut runs after each stored put.
type s3stub struct {
	clock *clock.Manual
	srv   *httptest.Server

	mu       sync.Mutex
	cond     condition
	box      bool
	failPut  func(key string) bool
	onPut    func(s *s3stub, key string)
	objects  map[string]stubObject
	puts     []string
	requests []string
	auth     []string
}

type stubObject struct {
	data []byte
	mod  time.Time
}

// TestMain keeps the AWS SDK off the developer's shared config and credentials and the EC2 metadata service, once for
// the process, so the stub's tests need no t.Setenv and can run in parallel.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "funcd")
	if err != nil {
		fmt.Fprintln(os.Stderr, "s3 stub TestMain:", err)
		os.Exit(2)
	}
	none := filepath.Join(dir, "none")
	for k, v := range map[string]string{
		"AWS_CONFIG_FILE":             none,
		"AWS_SHARED_CREDENTIALS_FILE": none,
		"AWS_EC2_METADATA_DISABLED":   "true",
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "s3 stub TestMain:", err)
			os.Exit(2)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func newS3Stub(t *testing.T, c *clock.Manual) *s3stub {
	t.Helper()
	s := &s3stub{clock: c, objects: map[string]stubObject{}}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *s3stub) url() string {
	return "s3://" + stubBucket + "?region=us-east-1&use_path_style=true&endpoint=" + s.srv.URL
}

// set changes the stub's behavior under its lock.
func (s *s3stub) set(f func(s *s3stub)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// seed stores an object as another writer would, unrecorded in puts.
func (s *s3stub) seed(key string, data []byte) {
	s.set(func(s *s3stub) { s.objects[key] = stubObject{data: data, mod: s.clock.Now()} })
}

// keys returns the stored keys under prefix, sorted.
func (s *s3stub) keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, k := range slices.Sorted(maps.Keys(s.objects)) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// putKeys returns the stored puts under prefix, in order.
func (s *s3stub) putKeys(prefix string) []string {
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

func (s *s3stub) seen() (requests, auth []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests), slices.Clone(s.auth)
}

func (s *s3stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"+stubBucket), "/")
	s.requests = append(s.requests, r.Method+" "+key)
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodGet && key == "":
		s.list(w, r.URL.Query().Get("prefix"))
	case r.Method == http.MethodPut:
		s.put(w, r, key, body)
	case s.box:
		s3Error(w, http.StatusForbidden, "AccessDenied")
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		o, ok := s.objects[key]
		if !ok {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
		w.Header().Set("Last-Modified", o.mod.UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"e"`)
		if r.Method == http.MethodGet {
			_, _ = w.Write(o.data)
		}
	case r.Method == http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		s3Error(w, http.StatusBadRequest, "BadRequest")
	}
}

func (s *s3stub) put(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") ||
		strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		body = decodeChunked(body)
	}
	conditional := r.Header.Get("If-None-Match") == "*"
	_, exists := s.objects[key]
	switch {
	case s.failPut != nil && s.failPut(key), s.box && strings.HasPrefix(key, "gen/verified/"):
		s3Error(w, http.StatusForbidden, "AccessDenied")
		return
	case conditional && s.cond == conditionRefused:
		s3Error(w, http.StatusNotImplemented, "NotImplemented")
		return
	case conditional && s.cond == conditionHonored && exists:
		s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
		return
	}
	s.objects[key] = stubObject{data: body, mod: s.clock.Now()}
	s.puts = append(s.puts, key)
	if s.onPut != nil {
		s.onPut(s, key)
	}
	w.Header().Set("ETag", `"e"`)
}

type listResult struct {
	XMLName     xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Name        string
	Prefix      string
	KeyCount    int
	MaxKeys     int
	IsTruncated bool
	Contents    []listEntry
}

type listEntry struct {
	Key          string
	LastModified string
	ETag         string
	Size         int
	StorageClass string
}

func (s *s3stub) list(w http.ResponseWriter, prefix string) {
	res := listResult{Name: stubBucket, Prefix: prefix, MaxKeys: 1000}
	for _, k := range slices.Sorted(maps.Keys(s.objects)) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		o := s.objects[k]
		res.Contents = append(res.Contents, listEntry{
			Key: k, LastModified: o.mod.UTC().Format("2006-01-02T15:04:05.000Z"), ETag: `"e"`, Size: len(o.data), StorageClass: "STANDARD",
		})
	}
	res.KeyCount = len(res.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
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

// credentialsFile writes an AWS shared credentials file whose [default] profile holds keyID.
func credentialsFile(t *testing.T, keyID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials")
	data := "[default]\naws_access_key_id = " + keyID + "\naws_secret_access_key = secret-" + keyID + "\n"
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	return path
}
