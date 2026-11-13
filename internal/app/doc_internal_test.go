package app

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The App's field descriptions name these reasons and conditions, so a renamed one fails here until they follow
// (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	spec, hooks := reflect.TypeFor[v1.AppSpec](), reflect.TypeFor[v1.AppHooks]()
	secrets := []string{ownedstore.ReasonSecretNotFound, ownedstore.ReasonSecretKeyMissing}
	for _, field := range []string{"KV", "Buckets", "SQL"} {
		doctag.Names(t, spec, field, reasonChildNotOwned)
	}
	doctag.Names(t, spec, "Secrets", secrets...)
	doctag.Names(t, spec, "Paused", string(condPaused))
	doctag.Names(t, hooks, "PreApply", reasonHookFailed)
	doctag.Names(t, hooks, "PostApply", reasonHookFailed)
	doctag.Names(t, reflect.TypeFor[v1.PlanPart](), "Reason", append(secrets, reasonChildNotOwned)...)
	doctag.Names(t, reflect.TypeFor[v1.AppChild](), "Reason", reasonRefNotFound, reasonProgressing, reasonChildNotOwned,
		reasonChildInvalid, reasonStepNotReady, reasonNotCurrent, reasonRunHeld, reasonInUse)
}
