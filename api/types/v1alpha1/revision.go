package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/pyvvo/funcd/api/fault"
)

// Revision is a namespaced, immutable snapshot of a Function's or a Job's spec at a point in time (ADR-0347 Decision 2).
// Status-bearing. Behavioral fields (frozen snapshot payload) owned by F13.
type Revision struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RevisionSpec   `json:"spec"`
	Status     RevisionStatus `json:"status,omitempty"`
}

// RevisionSpec holds the immutable identity + frozen snapshot of a revision: exactly one of Function and Job names
// whose spec it is. Number is the generation it snapshots. The Runtime/Handler/Image snapshot (F13/ADR-0020, flattened
// by ADR-0097) pins "what was validated ships" — immutable once stamped.
type RevisionSpec struct {
	Function    ObjectRef   `json:"function,omitzero" doc:"The Function whose spec this Revision snapshots; unset on a Job's Revision. Exactly one of function and job is set."`
	Job         ObjectName  `json:"job,omitempty" doc:"The Job whose spec this Revision snapshots, in the Revision's namespace; unset on a Function's Revision. A Job's Revision is named <job>-v<generation>. Exactly one of function and job is set."`
	Number      int64       `json:"number" minimum:"1" doc:"The generation of the spec that this Revision snapshots."`
	Runtime     RuntimeName `json:"runtime,omitempty" doc:"The runtime that the Revision's workers run: the spec.runtime of its Function or Job when funcd created the Revision."`
	Handler     string      `json:"handler,omitempty" pattern:"^[A-Za-z_][A-Za-z0-9_.]*$" doc:"The export that the Revision's workers call: the spec.handler of its Function or Job when funcd created the Revision."`
	Image       string      `json:"image,omitempty" doc:"The artifact that the Revision's workers run: the spec.image of its Function or Job when funcd created the Revision."`
	ImageDigest string      `json:"imageDigest,omitempty" pattern:"^sha256:[a-f0-9]{64}$" doc:"The digest that the Revision's workers pull the image by: the spec.imageDigest of its Function or Job, or the digest that its image resolved to when funcd created the Revision. It never changes, even when a tag moves."`
	// RuntimeImage is the image of the Addon runtime that serves the Revision (ADR-0344 Decision 8).
	RuntimeImage string `json:"runtimeImage,omitempty" doc:"The image of the Addon runtime that serves the Revision, which its workers run: the image of the Addon's entry when funcd created the Revision, or, for a Revision created before its runtime served it, when a pass first found the entry while no worker of the Revision ran. It never changes after: an Addon update moves no Revision. Empty for any other runtime."`
}

// RevisionStatus holds the observed state.
type RevisionStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Revision.
func (r *Revision) GroupVersionKind() GroupVersionKind { return KindRevision.GVK() }

// Validate performs envelope validation via the shared validateMeta helper, then checks that exactly one of
// spec.function and spec.job names the Revision's subject (ADR-0347 Decision 2).
func (r *Revision) Validate() error {
	const op = "Revision.Validate"
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindRevision); err != nil {
		return err
	}
	fn, job := r.Spec.Function != (ObjectRef{}), r.Spec.Job != ""
	if fn == job {
		return fault.Invalidf(op, "spec sets %s: want exactly one of function and job", bothOrNeither(fn, "function", "job"))
	}
	if job && !dnsLabel.MatchString(string(r.Spec.Job)) {
		return fault.Invalidf(op, "spec.job %q is not a valid DNS-1123 label", r.Spec.Job)
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (r *Revision) GetStatus() *Status { return &r.Status.Status }

// The shortening of a revision's name (ADR-0172 Decision 4): a DNS-1123 label, with 8 hex digits of SHA-256 over its
// owner's name when <owner>-<tail> is too long.
const (
	maxRevisionName = 63
	revisionHashLen = 8
)

// FunctionRevisionName is the name of a Function's Revision of generation (ADR-0020): <function>-<generation>, shortened
// with the generation last.
func FunctionRevisionName(function ObjectName, generation int64) ObjectName {
	return revisionLabel(string(function), strconv.FormatInt(generation, 10))
}

// JobRevisionName is the name of a Job's Revision of generation (ADR-0347 Decision 2): <job>-v<generation>, shortened
// as a Function's Revision name is, with -v<generation> last. A Function's Revision name ends in -<digits>, so the two
// never meet.
func JobRevisionName(job ObjectName, generation int64) ObjectName {
	return revisionLabel(string(job), "v"+strconv.FormatInt(generation, 10))
}

// revisionLabel is <name>-<tail> while that fits a DNS-1123 label, else name cut to fit, the first revisionHashLen hex
// digits of its SHA-256 and tail, so tail always ends it.
func revisionLabel(name, tail string) ObjectName {
	if len(name)+1+len(tail) <= maxRevisionName {
		return ObjectName(name + "-" + tail)
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:revisionHashLen] + "-" + tail
	return ObjectName(name[:maxRevisionName-len(suffix)] + suffix)
}
