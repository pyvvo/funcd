package gocloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
)

// answeringStore serves h and opens it with opts, a static key by default.
func answeringStore(t *testing.T, h http.HandlerFunc, opts OpenOptions) blob.Bucket {
	t.Helper()
	return answeringBucket(t, "b", h, opts)
}

// answeringBucket is answeringStore for a bucket of that name.
func answeringBucket(t *testing.T, bucket string, h http.HandlerFunc, opts OpenOptions) blob.Bucket {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if opts.CredentialsFile == "" {
		opts.Static = &StaticKey{ID: "id", Secret: "secret"}
	}
	b, err := OpenWith(context.Background(), "s3://"+bucket+"?region=us-east-1&use_path_style=true&endpoint="+srv.URL, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// s3Error is an S3 error answer of code, size bytes long.
func s3Error(code string, size int) []byte {
	head, tail := `<?xml version="1.0" encoding="UTF-8"?><Error><Code>`+code+`</Code><Message>`, `</Message></Error>`
	return []byte(head + strings.Repeat("x", size-len(head)-len(tail)) + tail)
}

// storeCalls are the calls that read an answer's body, as a Backup and a Restore make them.
func storeCalls() map[string]func(context.Context, blob.Bucket) error {
	return map[string]func(context.Context, blob.Bucket) error{
		"GetRange": func(ctx context.Context, b blob.Bucket) error {
			_, err := b.(blob.RangeReader).GetRange(ctx, "k", 0, 10)
			return err
		},
		"Put": func(ctx context.Context, b blob.Bucket) error {
			return b.Put(ctx, "k", []byte("v"), blob.PutOptions{IfNotExist: true})
		},
		"ListAfter": func(ctx context.Context, b blob.Bucket) error {
			_, _, err := b.ListAfter(ctx, "", "", 10)
			return err
		},
	}
}

func requireS3Error(t *testing.T, err error, code string, status int) {
	t.Helper()
	require.Error(t, err)
	var ae smithy.APIError
	require.True(t, errors.As(err, &ae), "%v", err)
	require.Equal(t, code, ae.ErrorCode())
	var re *smithyhttp.ResponseError
	require.True(t, errors.As(err, &re), "%v", err)
	require.Equal(t, status, re.HTTPStatusCode())
}

// A store's error keeps its status and its code at any size up to the limit, one far larger than a real answer included.
func TestStaticKeyAnswers_KeepTheirStatusAndCodeUpToTheLimit(t *testing.T) {
	t.Parallel()
	for name, call := range storeCalls() {
		for _, size := range []int{512, 16 << 10, maxAnswerBytes} {
			t.Run(name+" "+strconv.Itoa(size), func(t *testing.T) {
				t.Parallel()
				b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write(s3Error("AccessDenied", size))
				}, OpenOptions{})
				requireS3Error(t, call(context.Background(), b), "AccessDenied", http.StatusForbidden)
			})
		}
	}
	t.Run("a missing object at the limit", func(t *testing.T) {
		t.Parallel()
		b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(s3Error("NoSuchKey", maxAnswerBytes))
		}, OpenOptions{})
		_, err := b.(blob.RangeReader).GetRange(context.Background(), "k", 0, 10)
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%v", err)
	})
}

// An error of any size fails its call, having read no more of the body than the limit: the store stops sending when the
// client closes the connection.
func TestStaticKeyAnswers_ReadNoMoreThanTheLimit(t *testing.T) {
	t.Parallel()
	const body = 32 << 20
	for name, call := range storeCalls() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var sent atomic.Int64
			done := make(chan struct{}, 1)
			chunk := []byte(strings.Repeat("x", 64<<10))
			b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
				defer func() { done <- struct{}{} }()
				w.WriteHeader(http.StatusForbidden)
				for sent.Load() < body {
					n, err := w.Write(chunk)
					sent.Add(int64(n))
					if err != nil {
						return
					}
				}
			}, OpenOptions{})
			require.Error(t, call(context.Background(), b))
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("the store is still sending")
			}
			require.Less(t, sent.Load(), int64(body/2), "the client read the body whole")
		})
	}
}

// An upload's answer is bounded too when it succeeds: the store stops sending when the client closes the connection.
func TestStaticKeyAnswers_BoundAnUploadsAnswer(t *testing.T) {
	t.Parallel()
	const body = 32 << 20
	var sent atomic.Int64
	done := make(chan struct{}, 1)
	chunk := []byte(strings.Repeat(" ", 64<<10))
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		defer func() { done <- struct{}{} }()
		for sent.Load() < body {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}, OpenOptions{})
	require.NoError(t, b.Put(context.Background(), "k", []byte("v"), blob.PutOptions{}))
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the store is still sending")
	}
	require.Less(t, sent.Load(), int64(body/2), "the client read the answer whole")
}

// An object's content is not an answer the limit cuts: its reader bounds it.
func TestStaticKeyAnswers_LeaveAnObjectsContent(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("x", maxListPageBytes+1)
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(content))
	}, OpenOptions{})
	got, err := b.(blob.RangeReader).GetRange(context.Background(), "k", 0, int64(len(content)))
	require.NoError(t, err)
	require.Len(t, got, len(content))
}

// listingPage is one page of a listing of one key, padded to size bytes.
func listingPage(size int) []byte {
	head, tail := `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>b</Name><Contents><Key>a</Key><LastModified>2026-11-04T00:00:00.000Z</LastModified><Size>1</Size></Contents>`, `</ListBucketResult>`
	return []byte(head + strings.Repeat(" ", size-len(head)-len(tail)) + tail)
}

// A listing's page of maxListPageBytes is read, and a longer one fails its listing, having read no more than that.
func TestStaticKeyAnswers_BoundAListingPage(t *testing.T) {
	t.Parallel()
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(listingPage(maxListPageBytes))
	}, OpenOptions{})
	items, _, err := b.ListAfter(context.Background(), "", "", 10)
	require.NoError(t, err)
	require.Len(t, items, 1)

	const body = 2 * (maxListPageBytes + 32<<20)
	var sent atomic.Int64
	done := make(chan struct{}, 1)
	chunk := []byte(strings.Repeat(" ", 64<<10))
	long := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		defer func() { done <- struct{}{} }()
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>b</Name>`))
		for sent.Load() < body {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}, OpenOptions{})
	_, _, err = long.ListAfter(context.Background(), "", "", 10)
	require.Error(t, err)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the store is still sending")
	}
	require.Less(t, sent.Load(), int64(body/2), "the client read the page whole")
}

// A listed object with no modification time fails its listing.
func TestStaticKeyAnswers_FailAListedObjectWithNoTime(t *testing.T) {
	t.Parallel()
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>b</Name><Contents><Key>a</Key><Size>1</Size></Contents></ListBucketResult>`))
	}, OpenOptions{})
	_, _, err := b.ListAfter(context.Background(), "", "", 10)
	require.ErrorContains(t, err, "no modification time")
}

// A session's answer is bounded, as a directory bucket's name makes the client ask for one before its first request.
func TestStaticKeyAnswers_BoundASessionsAnswer(t *testing.T) {
	t.Parallel()
	const body = 32 << 20
	var sent atomic.Int64
	done := make(chan struct{}, 1)
	chunk := []byte(strings.Repeat("x", 64<<10))
	b := answeringBucket(t, "abc--use1-az4--x-s3", func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("session") {
			_, _ = w.Write(listingPage(1024))
			return
		}
		defer func() {
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><CreateSessionResult><Junk>`))
		for sent.Load() < body {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}, OpenOptions{})
	_, _, _ = b.ListAfter(context.Background(), "", "", 10)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the client asked for no session")
	}
	require.Less(t, sent.Load(), int64(body/2), "the client read the answer whole")
}

// A credentials file's client reads its answers whole.
func TestCredentialsFileAnswers_AreNotCut(t *testing.T) {
	t.Parallel()
	creds := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.WriteFile(creds, []byte("[default]\naws_access_key_id = id\naws_secret_access_key = secret\n"), 0o600))
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(s3Error("AccessDenied", 2*maxAnswerBytes))
	}, OpenOptions{CredentialsFile: creds})
	_, err := b.(blob.RangeReader).GetRange(context.Background(), "k", 0, 10)
	requireS3Error(t, err, "AccessDenied", http.StatusForbidden)
}
