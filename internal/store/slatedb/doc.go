// Package slatedb is the slatedb store.Engine (ADR-0006): the Rust LSM engine via
// the UniFFI/cgo binding (slatedb.io/slatedb-go), spanning memory / file / S3
// object backends from one library, selected by URL.
//
// The implementation lives in slatedb.go behind the "slatedb" build tag: it
// requires CGO_ENABLED=1 and the slatedb_uniffi native library on the loader
// path, so the default pure-Go build (and `just ci`) excludes it. Build the lib
// and run this lane with:
//
//	just slatedb-lib      # cargo-build libslatedb_uniffi from the pinned source
//	just test-slatedb     # CGO_ENABLED=1 go test -tags slatedb ./internal/store/slatedb/...
package slatedb
