package v1alpha1

import (
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

func lake(name string, lake LakeLocation, blob ...FunctionBlob) *Lake {
	return &Lake{
		TypeMeta:   TypeMeta{APIVersion: KindLake.GVK().APIVersion(), Kind: KindLake},
		ObjectMeta: ObjectMeta{Name: ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       LakeSpec{Location: lake, Bindings: LakeBindings{Blob: blob}},
	}
}

// scenario: lake-must-be-a-bound-blob (ADR-0086 structural rule) — Lake.Validate enforces
// the spec.blob structural rules (alias unique DNS-1123 label, prefix DNS-1123 label) AND that the
// lake (bucket, prefix) is one of the spec.blob bindings (so the engine gets the per-fn S3 keypair
// and can write its own DuckLake catalog). Cross-resource existence is the admission, not Validate.
func TestLakeValidate(t *testing.T) {
	goldLake := LakeLocation{Bucket: "lakehouse", Prefix: "gold"}

	// valid: the lake (lakehouse/gold) is a declared blob binding.
	if err := lake("ok", goldLake,
		FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}).Validate(); err != nil {
		t.Errorf("valid Lake rejected: %v", err)
	}

	// valid: extra bindings alongside the lake one.
	if err := lake("ok2", goldLake,
		FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"},
		FunctionBlob{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); err != nil {
		t.Errorf("valid Lake with extra bindings rejected: %v", err)
	}

	// invalid: the lake (bucket, prefix) is NOT one of spec.blob — the engine would get no keypair.
	if err := lake("unbound", goldLake,
		FunctionBlob{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("lake not in spec.blob: want Invalid, got %v", err)
	}

	// invalid: a Lake set with an empty spec.blob (implies the lake can't be bound).
	if err := lake("noblob", goldLake).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("Lake set with empty spec.blob: want Invalid, got %v", err)
	}

	// invalid: same bucket but a different prefix is not the lake binding.
	if err := lake("wrongprefix", goldLake,
		FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("lake prefix not bound: want Invalid, got %v", err)
	}

	// invalid: a duplicate spec.bindings.blob alias is still rejected (the structural loop runs first).
	if err := lake("dupalias", goldLake,
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "gold"},
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate spec.bindings.blob alias: want Invalid, got %v", err)
	}

	// invalid: a non-DNS-1123 lake prefix is rejected before the binding check.
	if err := lake("badprefix", LakeLocation{Bucket: "lakehouse", Prefix: "Bad_Prefix"},
		FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad lake prefix: want Invalid, got %v", err)
	}
}

// scenario: spec.ingress validate (ADR-0138, ADR-0241) — opt-in external exposure. Structural Validate checks the
// path shape and refuses auth; the route (to the PEP proxy) is programmed by the reconciler once Ready.
func TestLakeValidate_ingress(t *testing.T) {
	goldLake := LakeLocation{Bucket: "lakehouse", Prefix: "gold"}
	gold := FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}

	// valid: a rooted path (optionally with a host).
	ok := lake("exposed", goldLake, gold)
	ok.Spec.Ingress = &Ingress{Enabled: new(true), Path: "/lake/lake", Host: "lake.example"}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid spec.ingress rejected: %v", err)
	}

	// valid: nil ingress (internal-only, the default) is fine.
	if err := lake("internal", goldLake, gold).Validate(); err != nil {
		t.Errorf("nil spec.ingress rejected: %v", err)
	}

	// valid: no path is the default /lake/<name> (ADR-0241 Decision 3).
	empty := lake("emptypath", goldLake, gold)
	empty.Spec.Ingress = &Ingress{Enabled: new(true)}
	if err := empty.Validate(); err != nil {
		t.Errorf("spec.ingress without a path rejected: %v", err)
	}

	// invalid: an auth, since the PEP proxy authenticates (ADR-0241 Decision 9).
	authed := lake("authed", goldLake, gold)
	authed.Spec.Ingress = &Ingress{Enabled: new(true), Auth: &RouteAuth{Mode: AuthOpen}}
	if err := authed.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("spec.ingress.auth: want Invalid, got %v", err)
	}

	// invalid: a non-rooted path (no leading slash).
	unrooted := lake("unrooted", goldLake, gold)
	unrooted.Spec.Ingress = &Ingress{Enabled: new(true), Path: "lake/lake"}
	if err := unrooted.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("non-rooted spec.ingress.path: want Invalid, got %v", err)
	}
}

// scenario: spec.secrets/spec.config validate (ADR-0087) — each names a Secret/ConfigMap whose Data
// is injected into the engine env (the ADR-0057 convention). Structural Validate checks only that
// each is a valid DNS-1123 ObjectName; cross-resource existence + read authorization is the
// reconciler's resolution step, not Validate.
func TestLakeValidate_secrets_config(t *testing.T) {
	goldLake := LakeLocation{Bucket: "lakehouse", Prefix: "gold"}
	gold := FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}

	// valid: well-formed secret + config names.
	cs := lake("ok", goldLake, gold)
	cs.Spec.Bindings.Secret = []ObjectName{"lake-quack-token"}
	cs.Spec.Bindings.Config = []ObjectName{"lake-engine-config"}
	if err := cs.Validate(); err != nil {
		t.Errorf("valid secrets/config rejected: %v", err)
	}

	// valid: empty secrets/config (the common case — a provider needing no token/config).
	if err := lake("none", goldLake, gold).Validate(); err != nil {
		t.Errorf("empty secrets/config rejected: %v", err)
	}

	// invalid: a non-DNS-1123 secret name.
	bad := lake("badsecret", goldLake, gold)
	bad.Spec.Bindings.Secret = []ObjectName{"Not_A_Label"}
	if err := bad.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad secret name: want Invalid, got %v", err)
	}

	// invalid: a non-DNS-1123 config name.
	badc := lake("badconfig", goldLake, gold)
	badc.Spec.Bindings.Config = []ObjectName{"Bad_Config"}
	if err := badc.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad config name: want Invalid, got %v", err)
	}
}

// TestLakeValidate_messages pins the exact error of every Lake.Validate branch, and which
// error wins when an input breaks several rules, so validating the bindings through the shared FunctionSpec
// helper keeps every message byte-identical.
func TestLakeValidate_messages(t *testing.T) {
	t.Parallel()
	const op = "Lake.Validate: "
	gold := LakeLocation{Bucket: "lakehouse", Prefix: "gold"}
	bound := FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}
	bronze := FunctionBlob{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}
	blob := func(alias, prefix string) FunctionBlob {
		return FunctionBlob{Alias: alias, Bucket: "lakehouse", Prefix: prefix}
	}
	long := strings.Repeat("a", 64)

	tests := []struct {
		name string
		cs   *Lake
		want string
	}{
		{"valid", lake("ok", gold, bound), ""},
		{"valid extra binding", lake("ok", gold, bronze, bound), ""},
		{"valid secrets config ingress", lakeWith(lake("ok", gold, bound), func(c *Lake) {
			c.Spec.Bindings.Secret = []ObjectName{"lake-token"}
			c.Spec.Bindings.Config = []ObjectName{"lake-config", "lake-tuning"}
			c.Spec.Ingress = &Ingress{Enabled: new(true), Path: "/lake/lake"}
		}), ""},
		{"bad metadata name", lake("Bad_Name", gold, bound),
			`ObjectMeta.Validate: invalid name: ObjectName.Validate: "Bad_Name" is not a valid DNS label`},
		{"empty alias", lake("cs", gold, blob("", "gold")),
			op + `spec.bindings.blob alias "" is not a valid DNS-1123 label`},
		{"uppercase alias", lake("cs", gold, blob("Lake", "gold")),
			op + `spec.bindings.blob alias "Lake" is not a valid DNS-1123 label`},
		{"underscore alias", lake("cs", gold, blob("my_alias", "gold")),
			op + `spec.bindings.blob alias "my_alias" is not a valid DNS-1123 label`},
		{"leading hyphen alias", lake("cs", gold, blob("-a", "gold")),
			op + `spec.bindings.blob alias "-a" is not a valid DNS-1123 label`},
		{"64-char alias", lake("cs", gold, blob(long, "gold")),
			op + `spec.bindings.blob alias "` + long + `" is not a valid DNS-1123 label`},
		{"bad alias in second entry", lake("cs", gold, bound, blob("B", "bronze")),
			op + `spec.bindings.blob alias "B" is not a valid DNS-1123 label`},
		{"duplicate alias", lake("cs", gold, blob("a", "gold"), blob("a", "bronze")),
			op + `spec.bindings.blob alias "a" is duplicated`},
		{"duplicate alias not adjacent", lake("cs", gold, blob("a", "gold"), bronze, blob("a", "silver")),
			op + `spec.bindings.blob alias "a" is duplicated`},
		{"duplicate alias before its bad prefix", lake("cs", gold, blob("a", "gold"), blob("a", "Bad")),
			op + `spec.bindings.blob alias "a" is duplicated`},
		{"empty blob prefix", lake("cs", gold, blob("lake", "")),
			op + `spec.bindings.blob[lake].prefix "" is not a valid DNS-1123 label`},
		{"bad blob prefix", lake("cs", gold, blob("lake", "Bad_Prefix")),
			op + `spec.bindings.blob[lake].prefix "Bad_Prefix" is not a valid DNS-1123 label`},
		{"bad alias before bad prefix", lake("cs", gold, blob("Bad", "Bad")),
			op + `spec.bindings.blob alias "Bad" is not a valid DNS-1123 label`},
		{"blob before lake prefix", lake("cs", LakeLocation{Bucket: "lakehouse", Prefix: "Bad"}, blob("x_y", "gold")),
			op + `spec.bindings.blob alias "x_y" is not a valid DNS-1123 label`},
		{"bad lake prefix", lake("cs", LakeLocation{Bucket: "lakehouse", Prefix: "Bad_Prefix"}, bound),
			op + `spec.location.prefix "Bad_Prefix" is not a valid DNS-1123 label`},
		{"empty lake prefix", lake("cs", LakeLocation{Bucket: "lakehouse"}, bound),
			op + `spec.location.prefix "" is not a valid DNS-1123 label`},
		{"lake prefix not bound", lake("cs", gold, blob("lake", "bronze")),
			op + `spec.location ("lakehouse"/"gold") must be one of spec.bindings.blob (the lake prefix must be a bound, owned blob binding so the engine can write it)`},
		{"lake bucket not bound", lake("cs", LakeLocation{Bucket: "other", Prefix: "gold"}, bound),
			op + `spec.location ("other"/"gold") must be one of spec.bindings.blob (the lake prefix must be a bound, owned blob binding so the engine can write it)`},
		{"no blob", lake("cs", gold),
			op + `spec.location ("lakehouse"/"gold") must be one of spec.bindings.blob (the lake prefix must be a bound, owned blob binding so the engine can write it)`},
		{"bad secret", lakeWith(lake("cs", gold, bound), func(c *Lake) {
			c.Spec.Bindings.Secret = []ObjectName{"Not_A_Label"}
		}), op + `spec.bindings.secret entry "Not_A_Label" is not a valid DNS-1123 name`},
		{"bad config", lakeWith(lake("cs", gold, bound), func(c *Lake) {
			c.Spec.Bindings.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.bindings.config entry "Bad_Config" is not a valid DNS-1123 name`},
		{"empty config in second entry", lakeWith(lake("cs", gold, bound), func(c *Lake) {
			c.Spec.Bindings.Config = []ObjectName{"ok", ""}
		}), op + `spec.bindings.config entry "" is not a valid DNS-1123 name`},
		{"blob before config", lakeWith(lake("cs", gold, bound, blob("B", "gold")), func(c *Lake) {
			c.Spec.Bindings.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.bindings.blob alias "B" is not a valid DNS-1123 label`},
		{"lake prefix before config", lakeWith(lake("cs", LakeLocation{Bucket: "lakehouse", Prefix: "Bad"}, bound), func(c *Lake) {
			c.Spec.Bindings.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.location.prefix "Bad" is not a valid DNS-1123 label`},
		{"lake binding before config", lakeWith(lake("cs", gold, bronze), func(c *Lake) {
			c.Spec.Bindings.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.location ("lakehouse"/"gold") must be one of spec.bindings.blob (the lake prefix must be a bound, owned blob binding so the engine can write it)`},
		{"secret before config", lakeWith(lake("cs", gold, bound), func(c *Lake) {
			c.Spec.Bindings.Secret = []ObjectName{"Bad_Secret"}
			c.Spec.Bindings.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.bindings.secret entry "Bad_Secret" is not a valid DNS-1123 name`},
		{"config before ingress", lakeWith(lake("cs", gold, bound), func(c *Lake) {
			c.Spec.Bindings.Config = []ObjectName{"Bad_Config"}
			c.Spec.Ingress = &Ingress{Enabled: new(true), Path: "lake"}
		}), op + `spec.bindings.config entry "Bad_Config" is not a valid DNS-1123 name`},
		{"unrooted ingress path", lakeWith(lake("cs", gold, bound), func(c *Lake) {
			c.Spec.Ingress = &Ingress{Enabled: new(true), Path: "lake/lake"}
		}), op + `spec.ingress.path "lake/lake" must begin with '/'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ""
			if err := tt.cs.Validate(); err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Errorf("Validate() error\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func lakeWith(c *Lake, edit func(*Lake)) *Lake {
	edit(c)
	return c
}

// TestScenarioLakeKindRegistered covers scenario: lake-kind-registered (ADR-0261): Lake is a kind, and CatalogService,
// its name before the rename, is not.
func TestScenarioLakeKindRegistered(t *testing.T) {
	if err := KindLake.Validate(); err != nil {
		t.Fatalf("KindLake.Validate() = %v", err)
	}
	obj, ok := NewObject(KindLake)
	if !ok {
		t.Fatal("NewObject(Lake): not a kind")
	}
	if _, isLake := obj.(*Lake); !isLake {
		t.Fatalf("NewObject(Lake) = %T, want *Lake", obj)
	}
	if err := Kind("CatalogService").Validate(); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("Kind(CatalogService).Validate() = %v, want Invalid", err)
	}
	if _, ok := NewObject("CatalogService"); ok {
		t.Fatal("NewObject(CatalogService) still makes an object")
	}
}
