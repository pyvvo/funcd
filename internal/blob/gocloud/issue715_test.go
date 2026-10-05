package gocloud_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// Issue 715 / 768: fileblob pages by key while it walks in file-name order, so t/events.json, which sorts
// before the t/events/ directory's keys but is walked after them, was lost past the first 1000.
func TestIssue715_FileListKeepsEveryKey(t *testing.T) {
	testFileListKeepsEveryKey(t)
}

// scenario: file-list-keeps-every-key (ADR-0184).
func TestScenarioFileListKeepsEveryKey(t *testing.T) {
	testFileListKeepsEveryKey(t)
}

// scenario: list-after-contract (ADR-0184) — the contract suite, whose list-after case pins ListAfter on the
// memory and file drivers.
func TestScenarioListAfterContract(t *testing.T) {
	runContract(t)
}

func testFileListKeepsEveryKey(t *testing.T) {
	ctx := context.Background()
	b, err := gocloud.Open(ctx, gocloud.FileURL(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	want := make([]string, 0, 1001)
	for i := range 1000 {
		want = append(want, fmt.Sprintf("t/events/x%04d", i))
	}
	want = append(want, "t/events.json")
	for _, k := range want {
		require.NoError(t, b.Put(ctx, k, []byte("x"), blob.PutOptions{}))
	}
	slices.Sort(want)

	items, err := b.List(ctx, "t/")
	require.NoError(t, err)
	got := make([]string, 0, len(items))
	for _, it := range items {
		got = append(got, it.Key)
	}
	require.Contains(t, got, "t/events.json")
	require.Equal(t, want, got)
}
