package function

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/workernode/local"
)

// A member entry lets its key switch only when it reads ready with no dependency report (ADR-0224 Decision 3a).
func TestReadyForSwitch(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		m    memberHealth
		want bool
	}{
		"ready":                        {memberHealth{Name: "b", State: memberReady}, true},
		"loading":                      {memberHealth{Name: "b", State: "loading"}, false},
		"restarting":                   {memberHealth{Name: "b", State: "restarting"}, false},
		"failed":                       {memberHealth{Name: "b", State: memberFailed, Error: "TypeError"}, false},
		"no entry":                     {memberHealth{}, false},
		"ready with dependency report": {memberHealth{Name: "b", State: memberReady, Dependency: &local.DependencyReport{Kind: "kv", Binding: "t", Reason: "Forbidden"}}, false},
	} {
		require.Equal(t, tc.want, readyForSwitch(tc.m), name)
	}
}
