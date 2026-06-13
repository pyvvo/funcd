// Package store is the metastore port (ADR-0002 skeleton — real interface
// definition arrives with the store ADR, F05).
package store

import "context"

// Store is the metastore port. Real methods arrive with the store ADR.
type Store interface {
	// Ready reports whether the store is ready to serve requests.
	Ready(ctx context.Context) (bool, error)
}
