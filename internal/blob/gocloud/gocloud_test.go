package gocloud_test

import (
	"context"
	"testing"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/blobcontract"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// scenario: driver-conformance-parity — the gocloud driver passes the identical
// blob contract against both the memory (memblob) and file (fileblob) backends.
func TestScenario_DriverConformanceParity(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		blobcontract.RunContract(t, func(t *testing.T) blob.Bucket {
			b, err := gocloud.Open(context.Background(), "mem://")
			if err != nil {
				t.Fatalf("open mem bucket: %v", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			return b
		})
	})
	t.Run("file", func(t *testing.T) {
		blobcontract.RunContract(t, func(t *testing.T) blob.Bucket {
			b, err := gocloud.Open(context.Background(), "file://"+t.TempDir())
			if err != nil {
				t.Fatalf("open file bucket: %v", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			return b
		})
	})
}
