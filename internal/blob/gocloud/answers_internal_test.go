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
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if opts.CredentialsFile == "" {
		opts.Static = &StaticKey{ID: "id", Secret: "secret"}
	}
	b, err := OpenWith(context.Background(), "s3://b?region=us-east-1&use_path_style=true&endpoint="+srv.URL, opts)
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

// An object's content is not an answer the limit cuts: its reader bounds it.
func TestStaticKeyAnswers_LeaveAnObjectsContent(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("x", 2*maxAnswerBytes)
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(content))
	}, OpenOptions{})
	got, err := b.(blob.RangeReader).GetRange(context.Background(), "k", 0, int64(len(content)))
	require.NoError(t, err)
	require.Len(t, got, len(content))
}

// A credentials file is the operator's own store, whose client stays as it was.
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
