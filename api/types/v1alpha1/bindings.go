package v1alpha1

// FunctionBindings are what a Function's code uses (ADR-0246), named like the ctx accessors (ADR-0245). Each binding
// is its grant: with no matching entry, the accessor on that alias is refused (default-deny). Cross-resource validity
// (the store, bucket, database, lake, target, ConfigMap or Secret exists) is an admission; Validate checks the
// structural rules.
type FunctionBindings struct {
	// KV binds an alias to a (store, table) sub-domain of a KVStore in this namespace, read through ctx.kv (ADR-0073);
	// writes need the table's owner or a writer role (ADR-0136).
	KV []FunctionKV `json:"kv,omitempty"`
	// Blob binds an alias to a (bucket, prefix) sub-domain, read through ctx.blob and S3 (ADR-0080); writes need the
	// prefix's owner or a writer role.
	Blob []FunctionBlob `json:"blob,omitempty"`
	// SQL binds an alias to a SQLDatabase, read through ctx.sql (ADR-0233); writes need its owner or a writer role. A
	// missing database holds the Function Pending (SQLDatabaseNotFound).
	SQL []FunctionSQL `json:"sql,omitempty"`
	// Lake binds an alias to a Lake (ADR-0091): the worker gets FUNCD_LAKE_<ALIAS>_URL and _TOKEN, and
	// the reconcile waits, fail-closed, until the lake is Ready.
	Lake []FunctionLake `json:"lake,omitempty"`
	// Links binds an alias to a target Function, called through ctx.invoke and ctx.fetch (ADR-0064); with no matching
	// link the call is refused.
	Links []FunctionLink `json:"links,omitempty"`
	// Config names ConfigMaps whose Data is injected as env vars, before Secrets, so a bound Secret overrides a config
	// default (ADR-0093); a missing one fails closed (ConfigResolveFailed).
	Config []ObjectName `json:"config,omitempty"`
	// Secret names Secrets whose Data is injected as env vars (ADR-0057); only the names are stored, resolution is
	// PDP-authorized, and an unauthorized or missing Secret fails the Function closed.
	Secret []ObjectName `json:"secret,omitempty"`
}

// StepBindings are a Workflow step's bindings (ADR-0246): FunctionBindings without links, copied onto the step's
// materialized Function.
type StepBindings struct {
	KV     []FunctionKV   `json:"kv,omitempty"`
	Blob   []FunctionBlob `json:"blob,omitempty"`
	SQL    []FunctionSQL  `json:"sql,omitempty"`
	Lake   []FunctionLake `json:"lake,omitempty"`
	Config []ObjectName   `json:"config,omitempty"`
	Secret []ObjectName   `json:"secret,omitempty"`
}

// LakeBindings are what a Lake's engine uses (ADR-0246, ADR-0086/0087).
type LakeBindings struct {
	// Blob binds the lakehouse buckets the engine may read and write, projected onto its backing Function so ADR-0085
	// injects the keypair; the prefix the lake lives under must be owned by this service (single-writer).
	Blob []FunctionBlob `json:"blob"`
	// Config names ConfigMaps whose Data is injected into the engine as env vars: engine tuning (DUCKDB_*).
	Config []ObjectName `json:"config,omitempty"`
	// Secret names Secrets whose Data is injected into the engine as env vars: the Quack token (QUACK_TOKEN).
	Secret []ObjectName `json:"secret,omitempty"`
}

// FunctionBindings returns the step's bindings as a Function's, without links.
func (b StepBindings) FunctionBindings() FunctionBindings {
	return FunctionBindings{KV: b.KV, Blob: b.Blob, SQL: b.SQL, Lake: b.Lake, Config: b.Config, Secret: b.Secret}
}

// SiteBindings are a Site's bindings (ADR-0248 Decision 7): its served prefix, one alias-free entry.
type SiteBindings struct {
	Blob []BlobRef `json:"blob"` // exactly one
}

// BlobRef names a (bucket, prefix) sub-domain without an alias.
type BlobRef struct {
	Bucket ObjectName `json:"bucket"`
	Prefix string     `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}
