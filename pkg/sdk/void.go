package sdk

// VoidSchema is the canonical void side (ADR-0090/0122): a contract side that carries no meaningful payload is the
// explicit JSON Schema {"type":"null"}, never an omission. Kept in pkg/sdk (not imported from internal/artifact) so
// funcdctl and the SDK share it without a package-boundary violation; the value matches internal/artifact.VoidSchema.
const VoidSchema = `{"type":"null"}`
