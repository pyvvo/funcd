package function

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/contractcheck"
)

// openContractName is the contract a shim gets once funcd checks every call (ADR-0303 Decision 7): the delivered
// blob with both sides {}, ADR-0058's Json form, so the shim's validators accept everything. A dotfile, so the
// single-file artifact resolver never takes it for the handler (ADR-0123).
const openContractName = ".funcd-contract-open.json"

// reasonContractInvalid is the reason of a Function whose delivered contract does not compile (ADR-0303 Decision 3).
const reasonContractInvalid = "ContractInvalid"

// contractInvalidError is the materialize error of an artifact whose delivered contract does not compile.
type contractInvalidError struct {
	digest string
	err    error
}

func (e contractInvalidError) Error() string {
	return "artifact " + e.digest + " carries a contract funcd cannot compile: " + e.err.Error()
}

// contracts caches each artifact digest's compiled contract (ADR-0303 Decision 2): a digest names one blob.
type contracts struct {
	mu    sync.Mutex
	pairs map[string]*contractcheck.Pair
}

// compiled returns the compiled contract of the blob in dir, the bundle root of digest, from the cache when it holds
// it, keyed by the digest, or by the blob for a file artifact. A dir with no delivered blob has none.
func (cs *contracts) compiled(digest, dir string) (*contractcheck.Pair, error) {
	name, ok := contractFileIn(dir)
	if !ok {
		return nil, nil
	}
	key, blob := digest, []byte(nil)
	if key == "" { // a file artifact has no digest and can change in place, so its blob's content names it
		var err error
		if blob, err = os.ReadFile(filepath.Join(dir, name)); err != nil {
			return nil, fault.Wrapf(err, fault.Unavailable, "function.contracts", "read the delivered contract")
		}
		sum := sha256.Sum256(blob)
		key = dir + "@" + hex.EncodeToString(sum[:])
	}
	cs.mu.Lock()
	pair, ok := cs.pairs[key]
	cs.mu.Unlock()
	if ok {
		return pair, nil
	}
	if blob == nil {
		var err error
		if blob, err = os.ReadFile(filepath.Join(dir, name)); err != nil {
			return nil, fault.Wrapf(err, fault.Unavailable, "function.contracts", "read the delivered contract")
		}
	}
	var sides struct {
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(blob, &sides); err != nil {
		return nil, contractInvalidError{digest: digest, err: err}
	}
	bounds := contract.WithCallBounds(contractcheck.CallBudget, contractcheck.MaxDigits)
	in, err := contract.Compile(sides.Input, bounds)
	if err != nil {
		return nil, contractInvalidError{digest: digest, err: err}
	}
	out, err := contract.Compile(sides.Output, bounds)
	if err != nil {
		return nil, contractInvalidError{digest: digest, err: err}
	}
	pair = &contractcheck.Pair{Input: in, Output: out}
	cs.mu.Lock()
	if cs.pairs == nil {
		cs.pairs = map[string]*contractcheck.Pair{}
	}
	cs.pairs[key] = pair
	cs.mu.Unlock()
	return pair, nil
}

// ContractOf returns the compiled contract of the revision that serves a call to ref (ADR-0303 Decision 2): a pinned
// step's revision; else a pooled Function's status.currentRevision; else status.servingRevision, then
// status.currentRevision. fn is the Function the data plane read, or nil on a client path. An http Function, or one
// with no revision yet, has none.
func (r *Reconciler) ContractOf(ctx context.Context, ref activator.FunctionRef, fn *v1.Function) (*contractcheck.Pair, error) {
	const op = "function.ContractOf"
	if fn == nil {
		obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), ref.Namespace, ref.Name)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "function %s/%s", ref.Namespace, ref.Name)
		}
		fn, _ = obj.(*v1.Function)
	}
	if fn == nil || fn.Spec.Protocol.Normalized() == v1.ProtocolHTTP || r.materializer == nil {
		return nil, nil
	}
	rev := v1.ObjectName(ref.Revision)
	if rev == "" && fn.Status.Pool != "" {
		rev = v1.ObjectName(fn.Status.CurrentRevision) // a pool runs its members' current revisions
	}
	if rev == "" {
		rev = v1.ObjectName(fn.Status.ServingRevision)
	}
	if rev == "" {
		rev = v1.ObjectName(fn.Status.CurrentRevision)
	}
	if rev == "" {
		return nil, nil
	}
	tmpl, digest, err := r.revisionTemplate(ctx, fn, rev)
	if err != nil {
		return nil, err
	}
	mfn := *tmpl
	mfn.Spec.ImageDigest = digest
	path, err := r.materialize(ctx, &mfn)
	if err != nil {
		return nil, err
	}
	return r.contracts.compiled(digest, filepath.Dir(path))
}

// writeOpenContract writes the open contract beside the delivered blob in dir and returns its name, or "" when dir
// delivers none (ADR-0303 Decision 7). It keeps the delivered blob's dialect, and writes only when the file differs.
func writeOpenContract(dir string) (string, error) {
	const op = "function.writeOpenContract"
	name, ok := contractFileIn(dir)
	if !ok {
		return "", nil
	}
	blob, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "read the delivered contract")
	}
	var sides map[string]json.RawMessage
	if err := json.Unmarshal(blob, &sides); err != nil {
		return "", fault.Invalidf(op, "the delivered contract is not JSON: %v", err)
	}
	sides["input"], sides["output"] = json.RawMessage("{}"), json.RawMessage("{}")
	open, err := json.Marshal(sides)
	if err != nil {
		return "", fault.Internalf(op, "marshal the open contract: %v", err)
	}
	path := filepath.Join(dir, openContractName)
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, open) {
		return openContractName, nil
	}
	tmp, err := os.CreateTemp(dir, openContractName+".*")
	if err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "write the open contract")
	}
	_, werr := tmp.Write(open)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return "", fault.Wrapf(werr, fault.Unavailable, op, "write the open contract")
	}
	return openContractName, nil
}
