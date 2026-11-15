package blob

import "github.com/pyvvo/funcd/api/fault"

// The known causes of the blob service's errors (ADR-0330 Decision 2): internal/services/blob, the views of this package,
// the gocloud driver and the worker node's local API raise them, and each endpoint of that API that answers one declares
// it.
var (
	ErrCallerMissing = fault.NewKnownCause(fault.Forbidden, "blob.caller-missing",
		"The calling Function no longer exists, so it binds no prefix.",
		"Call from a Function that exists; a deleted Function's worker stops.")
	ErrNoBinding = fault.NewKnownCause(fault.Forbidden, "blob.no-binding",
		"The Function's spec.bindings.blob has no entry for the alias.",
		"Add the binding to spec.bindings.blob, or call an alias that the Function binds.")
	ErrBucketMissing = fault.NewKnownCause(fault.NotFound, "blob.bucket-missing",
		"The binding names a Bucket that does not exist.",
		"Create the Bucket, or bind one that exists.")
	ErrReadDenied = fault.NewKnownCause(fault.Forbidden, "blob.read-denied",
		"A Policy forbids the Function to read the prefix.",
		"Change or remove the Policy that forbids s3::read on the prefix.")
	ErrWriteDenied = fault.NewKnownCause(fault.Forbidden, "blob.write-denied",
		"The Function does not own the prefix, no RolesAssignment makes it a writer, a Policy forbids the write, or the "+
			"Bucket does not exist.",
		"Create the Bucket if it is missing; then write from the prefix's owner, or grant the Function s3::write with a "+
			"RolesAssignment, such as the Blob Data Writer role.")
	ErrObjectNotFound = fault.NewKnownCause(fault.NotFound, "blob.object-not-found",
		"The object does not exist.",
		"Write the object first, or handle its absence.")
	ErrObjectOverCap = fault.NewKnownCause(fault.PayloadTooLarge, "blob.object-over-cap",
		"The object is larger than the Bucket's spec.quota.maxObjectBytes.",
		"Write a smaller object, or raise the Bucket's spec.quota.maxObjectBytes.")
	ErrSignPutCapped = fault.NewKnownCause(fault.Forbidden, "blob.sign-put-capped",
		"The Bucket sets maxObjectBytes or a size quota, or the prefix sets a size quota, which funcd could not enforce "+
			"on the upload of a presigned PUT.",
		"Write the object with put, or sign a GET or a DELETE.")
	ErrSignUnsupported = fault.NewKnownCause(fault.Unavailable, "blob.sign-unsupported",
		"The blob store that funcd runs with signs no URL, as no store that funcd opens does.",
		"Read and write the object with get and put, or serve it from a Function.")
	ErrKeyNotUTF8 = fault.NewKnownCause(fault.Invalid, "blob.key-not-utf8",
		"The key is not valid UTF-8.",
		"Use a key of UTF-8 text.")
	ErrKeyReserved = fault.NewKnownCause(fault.Invalid, "blob.key-reserved",
		"funcd's file store refuses the key: it ends in .attrs or .funcd-tmp or holds __0x, which the store reserves, "+
			"or it has a . segment or a last segment of .., which the store would keep under another key's path.",
		"Use another key.")
	ErrKeyUnstorable = fault.NewKnownCause(fault.Invalid, "blob.key-unstorable",
		"funcd's file store cannot hold the key: the key or one of its segments is longer than the file system takes, "+
			"or the key needs the path of another object, as a/b does while a is an object.",
		"Use another key, or delete the object whose path the key needs.")
)
