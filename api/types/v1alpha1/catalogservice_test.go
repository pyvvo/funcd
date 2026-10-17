package v1alpha1

import (
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

func catalogService(name string, catalog CatalogRef, blob ...FunctionBlob) *CatalogService {
	return &CatalogService{
		TypeMeta:   TypeMeta{APIVersion: KindCatalogService.GVK().APIVersion(), Kind: KindCatalogService},
		ObjectMeta: ObjectMeta{Name: ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       CatalogServiceSpec{Blob: blob, Catalog: catalog},
	}
}

// scenario: catalog-must-be-a-bound-blob (ADR-0086 structural rule) — CatalogService.Validate enforces
// the spec.blob structural rules (alias unique DNS-1123 label, prefix DNS-1123 label) AND that the
// catalog (bucket, prefix) is one of the spec.blob bindings (so the engine gets the per-fn S3 keypair
// and can write its own DuckLake catalog). Cross-resource existence is the admission, not Validate.
func TestCatalogServiceValidate(t *testing.T) {
	goldCatalog := CatalogRef{Bucket: "lakehouse", Prefix: "gold"}

	// valid: the catalog (lakehouse/gold) is a declared blob binding.
	if err := catalogService("ok", goldCatalog,
		FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"}).Validate(); err != nil {
		t.Errorf("valid CatalogService rejected: %v", err)
	}

	// valid: extra bindings alongside the catalog one.
	if err := catalogService("ok2", goldCatalog,
		FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"},
		FunctionBlob{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); err != nil {
		t.Errorf("valid CatalogService with extra bindings rejected: %v", err)
	}

	// invalid: the catalog (bucket, prefix) is NOT one of spec.blob — the engine would get no keypair.
	if err := catalogService("unbound", goldCatalog,
		FunctionBlob{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("catalog not in spec.blob: want Invalid, got %v", err)
	}

	// invalid: a Catalog set with an empty spec.blob (implies the catalog can't be bound).
	if err := catalogService("noblob", goldCatalog).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("Catalog set with empty spec.blob: want Invalid, got %v", err)
	}

	// invalid: same bucket but a different prefix is not the catalog binding.
	if err := catalogService("wrongprefix", goldCatalog,
		FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("catalog prefix not bound: want Invalid, got %v", err)
	}

	// invalid: a duplicate spec.blob alias is still rejected (the structural loop runs first).
	if err := catalogService("dupalias", goldCatalog,
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "gold"},
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate spec.blob alias: want Invalid, got %v", err)
	}

	// invalid: a non-DNS-1123 catalog prefix is rejected before the binding check.
	if err := catalogService("badprefix", CatalogRef{Bucket: "lakehouse", Prefix: "Bad_Prefix"},
		FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad catalog prefix: want Invalid, got %v", err)
	}
}

// scenario: spec.ingress validate (ADR-0138, ADR-0241) — opt-in external exposure. Structural Validate checks the
// path shape and refuses auth; the route (to the PEP proxy) is programmed by the reconciler once Ready.
func TestCatalogServiceValidate_ingress(t *testing.T) {
	goldCatalog := CatalogRef{Bucket: "lakehouse", Prefix: "gold"}
	gold := FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"}

	// valid: a rooted pathPrefix (optionally with a host).
	ok := catalogService("exposed", goldCatalog, gold)
	ok.Spec.Ingress = &Ingress{Enabled: new(true), Path: "/catalog/lake", Host: "lake.example"}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid spec.ingress rejected: %v", err)
	}

	// valid: nil ingress (internal-only, the default) is fine.
	if err := catalogService("internal", goldCatalog, gold).Validate(); err != nil {
		t.Errorf("nil spec.ingress rejected: %v", err)
	}

	// valid: no path is the default /catalogservice/<name> (ADR-0241 Decision 3).
	empty := catalogService("emptypath", goldCatalog, gold)
	empty.Spec.Ingress = &Ingress{Enabled: new(true)}
	if err := empty.Validate(); err != nil {
		t.Errorf("spec.ingress without a path rejected: %v", err)
	}

	// invalid: an auth, since the PEP proxy authenticates (ADR-0241 Decision 9).
	authed := catalogService("authed", goldCatalog, gold)
	authed.Spec.Ingress = &Ingress{Enabled: new(true), Auth: &RouteAuth{Mode: AuthOpen}}
	if err := authed.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("spec.ingress.auth: want Invalid, got %v", err)
	}

	// invalid: a non-rooted path (no leading slash).
	unrooted := catalogService("unrooted", goldCatalog, gold)
	unrooted.Spec.Ingress = &Ingress{Enabled: new(true), Path: "catalog/lake"}
	if err := unrooted.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("non-rooted spec.ingress.path: want Invalid, got %v", err)
	}
}

// scenario: spec.secrets/spec.config validate (ADR-0087) — each names a Secret/ConfigMap whose Data
// is injected into the engine env (the ADR-0057 convention). Structural Validate checks only that
// each is a valid DNS-1123 ObjectName; cross-resource existence + read authorization is the
// reconciler's resolution step, not Validate.
func TestCatalogServiceValidate_secrets_config(t *testing.T) {
	goldCatalog := CatalogRef{Bucket: "lakehouse", Prefix: "gold"}
	gold := FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"}

	// valid: well-formed secret + config names.
	cs := catalogService("ok", goldCatalog, gold)
	cs.Spec.Secrets = []ObjectName{"lake-quack-token"}
	cs.Spec.Config = []ObjectName{"lake-engine-config"}
	if err := cs.Validate(); err != nil {
		t.Errorf("valid secrets/config rejected: %v", err)
	}

	// valid: empty secrets/config (the common case — a provider needing no token/config).
	if err := catalogService("none", goldCatalog, gold).Validate(); err != nil {
		t.Errorf("empty secrets/config rejected: %v", err)
	}

	// invalid: a non-DNS-1123 secret name.
	bad := catalogService("badsecret", goldCatalog, gold)
	bad.Spec.Secrets = []ObjectName{"Not_A_Label"}
	if err := bad.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad secret name: want Invalid, got %v", err)
	}

	// invalid: a non-DNS-1123 config name.
	badc := catalogService("badconfig", goldCatalog, gold)
	badc.Spec.Config = []ObjectName{"Bad_Config"}
	if err := badc.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad config name: want Invalid, got %v", err)
	}
}

// TestCatalogServiceValidate_messages pins the exact error of every CatalogService.Validate branch, and which
// error wins when an input breaks several rules, so validating the bindings through the shared FunctionSpec
// helper keeps every message byte-identical.
func TestCatalogServiceValidate_messages(t *testing.T) {
	t.Parallel()
	const op = "CatalogService.Validate: "
	gold := CatalogRef{Bucket: "lakehouse", Prefix: "gold"}
	bound := FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"}
	bronze := FunctionBlob{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}
	blob := func(alias, prefix string) FunctionBlob {
		return FunctionBlob{Alias: alias, Bucket: "lakehouse", Prefix: prefix}
	}
	long := strings.Repeat("a", 64)

	tests := []struct {
		name string
		cs   *CatalogService
		want string
	}{
		{"valid", catalogService("ok", gold, bound), ""},
		{"valid extra binding", catalogService("ok", gold, bronze, bound), ""},
		{"valid secrets config ingress", catalogWith(catalogService("ok", gold, bound), func(c *CatalogService) {
			c.Spec.Secrets = []ObjectName{"lake-token"}
			c.Spec.Config = []ObjectName{"lake-config", "lake-tuning"}
			c.Spec.Ingress = &Ingress{Enabled: new(true), Path: "/catalog/lake"}
		}), ""},
		{"bad metadata name", catalogService("Bad_Name", gold, bound),
			`ObjectMeta.Validate: invalid name: ObjectName.Validate: "Bad_Name" is not a valid DNS label`},
		{"empty alias", catalogService("cs", gold, blob("", "gold")),
			op + `spec.blob alias "" is not a valid DNS-1123 label`},
		{"uppercase alias", catalogService("cs", gold, blob("Catalog", "gold")),
			op + `spec.blob alias "Catalog" is not a valid DNS-1123 label`},
		{"underscore alias", catalogService("cs", gold, blob("my_alias", "gold")),
			op + `spec.blob alias "my_alias" is not a valid DNS-1123 label`},
		{"leading hyphen alias", catalogService("cs", gold, blob("-a", "gold")),
			op + `spec.blob alias "-a" is not a valid DNS-1123 label`},
		{"64-char alias", catalogService("cs", gold, blob(long, "gold")),
			op + `spec.blob alias "` + long + `" is not a valid DNS-1123 label`},
		{"bad alias in second entry", catalogService("cs", gold, bound, blob("B", "bronze")),
			op + `spec.blob alias "B" is not a valid DNS-1123 label`},
		{"duplicate alias", catalogService("cs", gold, blob("a", "gold"), blob("a", "bronze")),
			op + `spec.blob alias "a" is duplicated`},
		{"duplicate alias not adjacent", catalogService("cs", gold, blob("a", "gold"), bronze, blob("a", "silver")),
			op + `spec.blob alias "a" is duplicated`},
		{"duplicate alias before its bad prefix", catalogService("cs", gold, blob("a", "gold"), blob("a", "Bad")),
			op + `spec.blob alias "a" is duplicated`},
		{"empty blob prefix", catalogService("cs", gold, blob("catalog", "")),
			op + `spec.blob[catalog].prefix "" is not a valid DNS-1123 label`},
		{"bad blob prefix", catalogService("cs", gold, blob("catalog", "Bad_Prefix")),
			op + `spec.blob[catalog].prefix "Bad_Prefix" is not a valid DNS-1123 label`},
		{"bad alias before bad prefix", catalogService("cs", gold, blob("Bad", "Bad")),
			op + `spec.blob alias "Bad" is not a valid DNS-1123 label`},
		{"blob before catalog prefix", catalogService("cs", CatalogRef{Bucket: "lakehouse", Prefix: "Bad"}, blob("x_y", "gold")),
			op + `spec.blob alias "x_y" is not a valid DNS-1123 label`},
		{"bad catalog prefix", catalogService("cs", CatalogRef{Bucket: "lakehouse", Prefix: "Bad_Prefix"}, bound),
			op + `spec.catalog.prefix "Bad_Prefix" is not a valid DNS-1123 label`},
		{"empty catalog prefix", catalogService("cs", CatalogRef{Bucket: "lakehouse"}, bound),
			op + `spec.catalog.prefix "" is not a valid DNS-1123 label`},
		{"catalog prefix not bound", catalogService("cs", gold, blob("catalog", "bronze")),
			op + `spec.catalog ("lakehouse"/"gold") must be one of spec.blob bindings (the catalog prefix must be a bound, owned blob binding so the engine can write it)`},
		{"catalog bucket not bound", catalogService("cs", CatalogRef{Bucket: "other", Prefix: "gold"}, bound),
			op + `spec.catalog ("other"/"gold") must be one of spec.blob bindings (the catalog prefix must be a bound, owned blob binding so the engine can write it)`},
		{"no blob", catalogService("cs", gold),
			op + `spec.catalog ("lakehouse"/"gold") must be one of spec.blob bindings (the catalog prefix must be a bound, owned blob binding so the engine can write it)`},
		{"bad secret", catalogWith(catalogService("cs", gold, bound), func(c *CatalogService) {
			c.Spec.Secrets = []ObjectName{"Not_A_Label"}
		}), op + `spec.secrets entry "Not_A_Label" is not a valid DNS-1123 name`},
		{"bad config", catalogWith(catalogService("cs", gold, bound), func(c *CatalogService) {
			c.Spec.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.config entry "Bad_Config" is not a valid DNS-1123 name`},
		{"empty config in second entry", catalogWith(catalogService("cs", gold, bound), func(c *CatalogService) {
			c.Spec.Config = []ObjectName{"ok", ""}
		}), op + `spec.config entry "" is not a valid DNS-1123 name`},
		{"blob before config", catalogWith(catalogService("cs", gold, bound, blob("B", "gold")), func(c *CatalogService) {
			c.Spec.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.blob alias "B" is not a valid DNS-1123 label`},
		{"catalog prefix before config", catalogWith(catalogService("cs", CatalogRef{Bucket: "lakehouse", Prefix: "Bad"}, bound), func(c *CatalogService) {
			c.Spec.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.catalog.prefix "Bad" is not a valid DNS-1123 label`},
		{"catalog binding before config", catalogWith(catalogService("cs", gold, bronze), func(c *CatalogService) {
			c.Spec.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.catalog ("lakehouse"/"gold") must be one of spec.blob bindings (the catalog prefix must be a bound, owned blob binding so the engine can write it)`},
		{"secret before config", catalogWith(catalogService("cs", gold, bound), func(c *CatalogService) {
			c.Spec.Secrets = []ObjectName{"Bad_Secret"}
			c.Spec.Config = []ObjectName{"Bad_Config"}
		}), op + `spec.secrets entry "Bad_Secret" is not a valid DNS-1123 name`},
		{"config before ingress", catalogWith(catalogService("cs", gold, bound), func(c *CatalogService) {
			c.Spec.Config = []ObjectName{"Bad_Config"}
			c.Spec.Ingress = &Ingress{Enabled: new(true), Path: "lake"}
		}), op + `spec.config entry "Bad_Config" is not a valid DNS-1123 name`},
		{"unrooted ingress path", catalogWith(catalogService("cs", gold, bound), func(c *CatalogService) {
			c.Spec.Ingress = &Ingress{Enabled: new(true), Path: "catalog/lake"}
		}), op + `spec.ingress.path "catalog/lake" must begin with '/'`},
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

func catalogWith(c *CatalogService, edit func(*CatalogService)) *CatalogService {
	edit(c)
	return c
}
