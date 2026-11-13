package v1alpha1

// FunctionBindings are what a Function's code uses (ADR-0246), named like the ctx accessors (ADR-0245). Each binding
// is its grant: with no matching entry, the accessor on that alias is refused (default-deny). Cross-resource validity
// (the store, bucket, database, lake, target, ConfigMap or Secret exists) is an admission; Validate checks the
// structural rules.
type FunctionBindings struct {
	// KV binds an alias to a (store, table) sub-domain of a KVStore in this namespace, read through ctx.kv (ADR-0073);
	// writes need the table's owner or a writer role (ADR-0136).
	KV []FunctionKV `json:"kv,omitempty" doc:"The KV tables the code reaches through ctx.kv, each under its alias. The binding grants reads; only the table's owner and the writers that a RolesAssignment names may write. A store or table that does not exist yet holds the Function in Pending with KVStoreNotFound."`
	// Blob binds an alias to a (bucket, prefix) sub-domain, read through ctx.blob and S3 (ADR-0080); writes need the
	// prefix's owner or a writer role.
	Blob []FunctionBlob `json:"blob,omitempty" doc:"The Bucket prefixes the code reaches through ctx.blob, and over S3 while the S3 gateway is on, each under its alias. The binding grants reads; only the prefix's owner and the writers that a RolesAssignment names may write. A Bucket or prefix that does not exist yet holds the Function in Pending with BucketNotFound."`
	// SQL binds an alias to a SQLDatabase, read through ctx.sql (ADR-0233); writes need its owner or a writer role. A
	// missing database holds the Function Pending (SQLDatabaseNotFound).
	SQL []FunctionSQL `json:"sql,omitempty" doc:"The SQLDatabases the code queries through ctx.sql, each under its alias. The binding grants reads; only the database's owner and the writers that a RolesAssignment names may write. A database that does not exist yet holds the Function in Pending with SQLDatabaseNotFound."`
	// Lake binds an alias to a Lake (ADR-0091): the worker gets FUNCD_LAKE_<ALIAS>_URL and _TOKEN, and
	// the reconcile waits, fail-closed, until the lake is Ready.
	Lake []FunctionLake `json:"lake,omitempty" doc:"The Lakes the code queries, each under its alias. The Function waits in Pending with LakeNotReady until every bound Lake is Ready."`
	// Links binds an alias to a target Function, called through ctx.invoke and ctx.fetch (ADR-0064); with no matching
	// link the call is refused.
	Links []FunctionLink `json:"links,omitempty" doc:"The Functions the code calls, each under its alias: through ctx.invoke one whose protocol is cloudevent, through ctx.fetch one whose protocol is http. Each target must exist in the namespace when this Function is applied, the namespace's links form no cycle, and a Function that a link targets cannot be deleted until the link goes."`
	// Config names ConfigMaps whose Data is injected as env vars, before Secrets, so a bound Secret overrides a config
	// default (ADR-0093); a missing one fails closed (ConfigResolveFailed).
	Config []ObjectName `json:"config,omitempty" doc:"ConfigMaps whose keys each worker gets as environment variables when it starts, a later ConfigMap's value winning for a key that both hold. A key that starts with FUNCD_, or that a bound Secret holds, is left out, and a missing ConfigMap holds the Function Failed with ConfigResolveFailed."`
	// Secret names Secrets whose Data is injected as env vars (ADR-0057); only the names are stored, resolution is
	// PDP-authorized, and an unauthorized or missing Secret fails the Function closed.
	Secret []ObjectName `json:"secret,omitempty" doc:"Secrets whose keys the code reads through ctx.secret, on each read and with no cache, a later Secret's value winning for a key that both hold. No value reaches the worker's environment, and no worker starts until each one can be read."`
}

// StepBindings are a Workflow step's bindings (ADR-0246): FunctionBindings without links, copied onto the step's
// materialized Function.
type StepBindings struct {
	KV     []FunctionKV   `json:"kv,omitempty" doc:"The KV tables the step's code reaches through ctx.kv, each under its alias; each store must be declared at the root, in kv. The binding grants reads; only the table's owner and the writers that a RolesAssignment names may write."`
	Blob   []FunctionBlob `json:"blob,omitempty" doc:"The Bucket prefixes the step's code reaches through ctx.blob, and over S3 while the S3 gateway is on, each under its alias; each Bucket must be declared at the root, in buckets. The binding grants reads; only the prefix's owner and the writers that a RolesAssignment names may write."`
	SQL    []FunctionSQL  `json:"sql,omitempty" doc:"The SQLDatabases the step's code queries through ctx.sql, each under its alias; each must be declared at the root, in sql. The binding grants reads; only the database's owner and the writers that a RolesAssignment names may write."`
	Lake   []FunctionLake `json:"lake,omitempty" doc:"The Lakes the step's code queries, each under its alias; each must be declared at the root, in lakes. The step's Function waits until every bound Lake is Ready."`
	Config []ObjectName   `json:"config,omitempty" doc:"ConfigMaps whose keys each worker of the step gets as environment variables when it starts; each must be declared at the root, in configMaps. A key that starts with FUNCD_, or that a bound Secret holds, is left out."`
	Secret []ObjectName   `json:"secret,omitempty" doc:"Secrets whose keys the step's code reads through ctx.secret, on each read; each must be declared at the root, in secrets. No value reaches the worker's environment."`
}

// LakeBindings are what a Lake's engine uses (ADR-0246, ADR-0086/0087).
type LakeBindings struct {
	// Blob binds the lakehouse buckets the engine may read and write, projected onto its backing Function so ADR-0085
	// injects the keypair; the prefix the lake lives under must be owned by this service (single-writer).
	Blob []FunctionBlob `json:"blob" doc:"The Bucket prefixes the engine may read, each under an alias. It writes only a prefix whose owner is this Lake, and the Lake's location must be one of them."`
	// Config names ConfigMaps whose Data is injected into the engine as env vars: engine tuning (DUCKDB_*).
	Config []ObjectName `json:"config,omitempty" doc:"ConfigMaps whose data the engine gets as environment variables. Only QUACK_TOKEN and the DUCKDB_ keys pass, DUCKDB_EXTENSION_DIRECTORY excepted, and a bound Secret's value wins over a ConfigMap's for the same key. A missing ConfigMap holds the Lake in Pending, with the reason BindingResolveFailed."`
	// Secret names Secrets whose Data is injected into the engine as env vars: the Quack token (QUACK_TOKEN).
	Secret []ObjectName `json:"secret,omitempty" doc:"Secrets whose data the engine gets as environment variables, such as its QUACK_TOKEN; the same keys pass as for config. funcd asks Cedar, as the Lake, for secret::read on each: a forbidden Secret holds the Lake in Pending, with the reason BindingResolveFailed, as does a missing one in a lake that another resource declares. A changed value replaces the engine."`
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
