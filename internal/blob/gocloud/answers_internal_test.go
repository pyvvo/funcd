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
	content := strings.Repeat("x", maxListBytes+1)
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(content))
	}, OpenOptions{})
	got, err := b.(blob.RangeReader).GetRange(context.Background(), "k", 0, int64(len(content)))
	require.NoError(t, err)
	require.Len(t, got, len(content))
}

// listedObject is a page's entry for key, which XML escapes as the caller wrote it.
func listedObject(key string) string {
	return `<Contents><Key>` + key + `</Key><LastModified>2026-11-04T00:00:00.000Z</LastModified><Size>1</Size></Contents>`
}

// listingPage is one page of a listing of one key, padded to size bytes.
func listingPage(size int) []byte {
	head, tail := `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>b</Name>`+listedObject("a"), `</ListBucketResult>`
	return []byte(head + strings.Repeat(" ", size-len(head)-len(tail)) + tail)
}

// truncatedPage is a page that holds contents and says more follow.
func truncatedPage(contents string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>b</Name><IsTruncated>true</IsTruncated>` +
		`<NextContinuationToken>t</NextContinuationToken>` + contents + `</ListBucketResult>`)
}

// One ListAfter reads a page that spends its budget to the byte, and a page one byte longer fails it. So does one cut before its
// root, which would otherwise read as an empty listing.
func TestStaticKeyAnswers_FailAListingThatSpendsItsBudget(t *testing.T) {
	t.Parallel()
	whole := maxListBytes - listPageCharge
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(listingPage(whole))
	}, OpenOptions{})
	items, _, err := b.ListAfter(context.Background(), "", "", 10)
	require.NoError(t, err)
	require.Len(t, items, 1)

	for name, page := range map[string][]byte{
		"one byte over":     listingPage(whole + 1),
		"before its root":   []byte(strings.Repeat(" ", whole+1)),
		"far past its size": []byte(strings.Repeat(" ", 3*maxListBytes)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(page) }, OpenOptions{})
			items, more, err := b.ListAfter(context.Background(), "", "", 10)
			require.ErrorContains(t, err, "pages of one listing")
			require.Empty(t, items)
			require.False(t, more)
		})
	}
}

// One ListAfter has one budget across its pages, however its store cuts them.
func TestStaticKeyAnswers_SpendOneBudgetAcrossAListingsPages(t *testing.T) {
	t.Parallel()
	const pageBytes = 256 << 10
	page := truncatedPage(listedObject("a") + strings.Repeat(" ", pageBytes))
	var requests atomic.Int64
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(page)
	}, OpenOptions{})
	_, _, err := b.ListAfter(context.Background(), "", "", 1000)
	require.ErrorContains(t, err, "pages of one listing")
	require.LessOrEqual(t, requests.Load(), int64(maxListBytes/pageBytes+1))
}

// Pages with a token and no key end through the same budget.
func TestStaticKeyAnswers_EndAListingOfEmptyPages(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(truncatedPage(""))
	}, OpenOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _, err := b.ListAfter(ctx, "", "", 1000)
	require.ErrorContains(t, err, "pages of one listing")
	require.LessOrEqual(t, requests.Load(), int64(maxListBytes/listPageCharge+1))
}

// A fill of 1000 of the longest keys S3 allows, of a character XML escapes, stays within the budget in one page and in 1000.
func TestStaticKeyAnswers_ReadARealListingHoweverItsStoreCutsIt(t *testing.T) {
	t.Parallel()
	object := listedObject(strings.Repeat("&amp;", 1024))
	for name, h := range map[string]http.HandlerFunc{
		"one page": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>b</Name>` + strings.Repeat(object, 1000) + `</ListBucketResult>`))
		},
		"a page per key": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(truncatedPage(object)) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := answeringStore(t, h, OpenOptions{})
			items, _, err := b.ListAfter(context.Background(), "", "", 1000)
			require.NoError(t, err)
			require.Len(t, items, 1000)
		})
	}
}

// A listed object with no modification time fails its listing, wherever it sits on its page.
func TestStaticKeyAnswers_FailAListedObjectWithNoTime(t *testing.T) {
	t.Parallel()
	for name, contents := range map[string]string{
		"the only object": `<Contents><Key>a</Key><Size>1</Size></Contents>`,
		"a later object":  listedObject("a") + `<Contents><Key>b</Key><Size>1</Size></Contents>`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := answeringStore(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>b</Name>` + contents + `</ListBucketResult>`))
			}, OpenOptions{})
			_, _, err := b.ListAfter(context.Background(), "", "", 10)
			require.ErrorContains(t, err, "no modification time")
		})
	}
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
