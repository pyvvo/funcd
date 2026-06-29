package blob

import "context"

// RangeReader is an OPTIONAL capability a blob.Bucket driver MAY implement: a
// byte-range read of an object (ADR-0080). The s3gateway type-asserts a Bucket to
// RangeReader to serve DuckDB's ranged Parquet reads efficiently and FALLS BACK to
// a full Get+slice when the driver does not implement it. Keeping it a separate,
// optional interface keeps the blob.Bucket port minimal and the contract tests
// additive.
type RangeReader interface {
	// GetRange returns bytes [offset, offset+length) of the object at key.
	// A negative length means "to end". offset must be >= 0. fault.NotFound if the
	// key is absent; fault.Invalid for a negative offset.
	GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error)
}
