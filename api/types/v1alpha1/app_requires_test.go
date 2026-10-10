package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

// ADR-0219 Decision 1: an empty range accepts any version, even none; a range matches only a SemVer 2.0.0 version
// that it accepts, with the library's prerelease rule.
func TestAppRequirementMatches(t *testing.T) {
	for _, tc := range []struct {
		rng, version string
		want         bool
	}{
		{"", "", true},
		{"", "beta", true},
		{"", "3.0.0", true},
		{"^2.0.0", "2.1.0", true},
		{"^2.0.0", "1.9.0", false},
		{"^2.0.0", "3.0.0", false},
		{"^2.0.0", "1.2", false},
		{"^2.0.0", "v2.1.0", false},
		{"^2.0.0", "2.1.0-rc.1", false},
		{"^2.0.0", "", false},
		{"^2.0.0", "beta", false},
		{">=2.1.0-rc.0 <3.0.0", "2.1.0-rc.1", true},
	} {
		if got := (AppRequirement{App: "lakehouse", Version: tc.rng}).Matches(tc.version); got != tc.want {
			t.Errorf("range %q, version %q: Matches = %v, want %v", tc.rng, tc.version, got, tc.want)
		}
	}
}

func requiring(reqs ...AppRequirement) func(*App) {
	return func(a *App) { a.Spec.Requires = reqs }
}

func TestAppValidateAcceptsRequires(t *testing.T) {
	for name, mutate := range map[string]func(*App){
		"a range":             requiring(AppRequirement{App: "lakehouse", Version: "^2.0.0"}),
		"no range":            requiring(AppRequirement{App: "lakehouse"}),
		"two Apps":            requiring(AppRequirement{App: "lakehouse", Version: ">=1.2.0 <3.0.0"}, AppRequirement{App: "audit", Version: "~1.4"}),
		"a free spec.version": func(a *App) { a.Spec.Version = "beta" },
	} {
		t.Run(name, func(t *testing.T) {
			if err := todoApp(mutate).Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestAppValidateRequiresRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*App)
		want   string
	}{
		{"an empty app", requiring(AppRequirement{Version: "^2.0.0"}), `spec.requires[0].app "" is not a valid DNS-1123 label`},
		{"an invalid app", requiring(AppRequirement{App: "Lake_House"}), `spec.requires[0].app "Lake_House" is not a valid DNS-1123 label`},
		{"a repeated app", requiring(AppRequirement{App: "lakehouse", Version: "^2.0.0"}, AppRequirement{App: "lakehouse", Version: "^3.0.0"}),
			`spec.requires[1] repeats the app "lakehouse" of spec.requires[0]`},
		{"a bad range", requiring(AppRequirement{App: "lakehouse", Version: "two"}), `spec.requires[0].version "two" is not a version range`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := todoApp(tc.mutate).Validate()
			if fault.KindOf(err) != fault.Invalid {
				t.Fatalf("Validate: %v, want an Invalid fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate: %v, want %q", err, tc.want)
			}
		})
	}
}

// ADR-0219 Contracts: requires, status.requires and status.requiredBy are additive; an App without them marshals as
// before.
func TestAppRequiresJSON(t *testing.T) {
	spec, err := json.Marshal(AppSpec{Version: "1.3.0"})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if string(spec) != `{"version":"1.3.0"}` {
		t.Fatalf("spec without requires = %s", spec)
	}
	empty, err := json.Marshal(AppStatus{})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(empty), "requires") || strings.Contains(string(empty), "requiredBy") {
		t.Fatalf("status without requirements = %s", empty)
	}
	spec, err = json.Marshal(AppSpec{Version: "1.3.0", Requires: []AppRequirement{{App: "lakehouse", Version: "^2.0.0"}, {App: "audit"}}})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if want := `{"version":"1.3.0","requires":[{"app":"lakehouse","version":"^2.0.0"},{"app":"audit"}]}`; string(spec) != want {
		t.Fatalf("spec = %s, want %s", spec, want)
	}
	st, err := json.Marshal(AppStatus{
		Requires:   []AppRequirementState{{App: "lakehouse", Version: "1.9.0"}, {App: "audit", Met: true}},
		RequiredBy: []ObjectName{"billing"},
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if want := `"requires":[{"app":"lakehouse","version":"1.9.0","met":false},{"app":"audit","met":true}],"requiredBy":["billing"]`; !strings.Contains(string(st), want) {
		t.Fatalf("status = %s, want it to hold %s", st, want)
	}
}
