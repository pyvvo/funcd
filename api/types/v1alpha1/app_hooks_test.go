package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// hookedApp is ADR-0214's fixture: todoApp with the Function todo-migrate, bound to table todos of todo-store, as its
// pre-hook.
func hookedApp(mutate func(*App)) *App {
	return todoApp(func(a *App) {
		a.Spec.Functions = append(a.Spec.Functions, AppFunction{Name: "todo-migrate", FunctionSpec: FunctionSpec{
			Runtime: "nodejs22",
			Handler: "index.handler",
			Image:   "oci-layout://todo-migrate:1",
			KV:      []FunctionKV{{Alias: "store", Store: "todo-store", Table: "todos"}},
		}})
		a.Spec.Hooks = &AppHooks{PreApply: []AppHook{{Function: "todo-migrate"}}}
		if mutate != nil {
			mutate(a)
		}
	})
}

func TestAppValidateAcceptsHooks(t *testing.T) {
	for name, a := range map[string]*App{
		"a pre-hook": hookedApp(nil),
		"one name in both lists": hookedApp(func(a *App) {
			a.Spec.Hooks.PostApply = []AppHook{{Function: "todo-migrate"}, {Function: "todo-api"}}
		}),
		"a pre-hook linking to a ref entry": hookedApp(func(a *App) {
			a.Spec.Functions = append(a.Spec.Functions, AppFunction{Ref: "mailer"})
			a.Spec.Functions[1].Links = []FunctionLink{{Alias: "mail", Target: "mailer"}}
		}),
		"a pre-hook linking to another pre-hook": hookedApp(func(a *App) {
			a.Spec.Functions[1].Links = []FunctionLink{{Alias: "api", Target: "todo-api"}}
			a.Spec.Hooks.PreApply = append(a.Spec.Hooks.PreApply, AppHook{Function: "todo-api"})
		}),
		"a pre-hook linking outside the App": hookedApp(func(a *App) {
			a.Spec.Functions[1].Links = []FunctionLink{{Alias: "audit", Target: "audit"}}
		}),
		"a post-hook linking to any entry": hookedApp(func(a *App) {
			a.Spec.Functions[1].Links = []FunctionLink{{Alias: "api", Target: "todo-api"}}
			a.Spec.Hooks = &AppHooks{PostApply: []AppHook{{Function: "todo-migrate"}}}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := a.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestAppValidateHookRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*App)
		want   string
	}{
		{"a name that is no functions entry", func(a *App) { a.Spec.Hooks.PreApply[0].Function = "x" },
			`spec.hooks.preApply[0].function "x" is not a function of this App`},
		{"a ref entry", func(a *App) {
			a.Spec.Functions = append(a.Spec.Functions, AppFunction{Ref: "mailer"})
			a.Spec.Hooks.PostApply = []AppHook{{Function: "mailer"}}
		}, `spec.hooks.postApply[0].function "mailer" is not a function of this App`},
		{"a name of another section", func(a *App) { a.Spec.Hooks.PreApply[0].Function = "todo-store" },
			`spec.hooks.preApply[0].function "todo-store" is not a function of this App`},
		{"a name twice in one list", func(a *App) {
			a.Spec.Hooks.PreApply = append(a.Spec.Hooks.PreApply, AppHook{Function: "todo-migrate"})
		}, `spec.hooks.preApply[1].function repeats "todo-migrate" of spec.hooks.preApply[0].function`},
		{"a pre-hook linking to an entry written after the pre-hooks", func(a *App) {
			a.Spec.Functions[1].Links = []FunctionLink{{Alias: "api", Target: "todo-api"}}
		}, `spec.hooks.preApply[0].function "todo-migrate" links to "todo-api", written after the pre-hooks`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := hookedApp(tc.mutate).Validate()
			if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate: %v, want an Invalid fault containing %q", err, tc.want)
			}
		})
	}
}

func TestAppRevisionHooksJSON(t *testing.T) {
	end := NewTimestamp(time.Date(2026, 10, 10, 9, 30, 0, 0, time.UTC))
	in := todoRevision(func(r *AppRevision) {
		r.Spec.HookInput = &AppHookInput{Event: "upgrade", App: "todo", From: "todo-3", To: "todo-4", FromVersion: "3.0.0", ToVersion: "4.0.0"}
		r.Status.Hooks = []AppHookCall{{Point: "preApply", Function: "todo-migrate", Invocation: "inv-0123456789abcdef0123", Phase: PhaseReady, EndTime: end}}
	})
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"hookInput":{"event":"upgrade","app":"todo","from":"todo-3","to":"todo-4","fromVersion":"3.0.0","toVersion":"4.0.0"}`,
		`"hooks":[{"point":"preApply","function":"todo-migrate","invocation":"inv-0123456789abcdef0123","phase":"Ready","endTime":"2026-10-10T09:30:00.000Z"}]`,
	} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("%s does not hold %s", b, want)
		}
	}
	install, err := json.Marshal(AppHookInput{Event: "install", App: "todo", To: "todo-1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(install) != `{"event":"install","app":"todo","to":"todo-1"}` {
		t.Fatalf("an install's input is %s: from and fromVersion are left out", install)
	}
}
