package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

func runtimeConfigSite(mutate func(*Site)) *Site {
	s := &Site{TypeMeta: TypeMeta{APIVersion: KindSite.GVK().APIVersion(), Kind: KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = "web", "default", "rg1"
	s.Spec = SiteSpec{
		Image:         "registry.example/web:1",
		Bucket:        SiteBucket{Name: "web"},
		Prefix:        "web",
		Ingress:       SiteIngress{Host: "web.test"},
		RuntimeConfig: &SiteRuntimeConfig{Values: map[string]string{"API_URL": "https://api.example"}},
	}
	if mutate != nil {
		mutate(s)
	}
	return s
}

// ADR-0239 Decision 7: a key follows the ConfigMap key rule, configMap is an object name, no ingress rule claims the
// generated file's path (a trailing slash matches it), and values alone render within the cap.
func TestSiteRuntimeConfigValidate(t *testing.T) {
	if err := runtimeConfigSite(nil).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Site)
		want   string
	}{
		{"a key that is not an identifier", func(s *Site) { s.Spec.RuntimeConfig.Values["api-url"] = "x" }, `"api-url"`},
		{"a configMap that is not an object name", func(s *Site) { s.Spec.RuntimeConfig.ConfigMap = "Flags_1" }, "spec.runtimeConfig.configMap"},
		{"a rule at the file's path", func(s *Site) {
			s.Spec.Ingress.Rules = []SiteRule{{Path: "/config.json", Prefix: "data"}}
		}, `"/config.json"`},
		{"a rule at the file's path with a trailing slash", func(s *Site) {
			s.Spec.Ingress.Rules = []SiteRule{{Path: "/config.json/", Prefix: "data"}}
		}, `"/config.json/"`},
		{"values over the cap", func(s *Site) {
			s.Spec.RuntimeConfig.Values["BIG"] = strings.Repeat("x", SiteRuntimeConfigMaxBytes)
		}, "32768"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runtimeConfigSite(tc.mutate).Validate()
			if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate: %v, want an Invalid fault naming %s", err, tc.want)
			}
		})
	}
	withoutConfig := runtimeConfigSite(func(s *Site) {
		s.Spec.RuntimeConfig = nil
		s.Spec.Ingress.Rules = []SiteRule{{Path: "/config.json", Prefix: "data"}}
	})
	if err := withoutConfig.Validate(); err != nil {
		t.Fatalf("a rule at /config.json without runtimeConfig must stay valid (the workaround): %v", err)
	}
}

// The file moves with the bundle: under EffectivePath, for a host, a path mount and the by-name default.
func TestSiteRuntimeConfigPath(t *testing.T) {
	for _, tc := range []struct {
		ingress SiteIngress
		want    string
	}{
		{SiteIngress{Host: "web.test"}, "/config.json"},
		{SiteIngress{Path: "/bi"}, "/bi/config.json"},
		{SiteIngress{}, "/site/web/config.json"},
	} {
		s := runtimeConfigSite(func(s *Site) { s.Spec.Ingress = tc.ingress })
		if got := s.RuntimeConfigPath(); got != tc.want {
			t.Fatalf("RuntimeConfigPath(%+v) = %q, want %q", tc.ingress, got, tc.want)
		}
	}
}

// Decision 2: one JSON object of string members in key order; no value breaks out of it.
func TestRenderSiteRuntimeConfig(t *testing.T) {
	if got := string(RenderSiteRuntimeConfig(nil)); got != "{}" {
		t.Fatalf("nil members = %s, want {}", got)
	}
	if got := string(RenderSiteRuntimeConfig(map[string]string{"B": "2", "A": "1"})); got != `{"A":"1","B":"2"}` {
		t.Fatalf("members = %s, want key order", got)
	}
	tricky := map[string]string{"X": "\"}</script>,{\"Y\":\"1"}
	var back map[string]string
	if err := json.Unmarshal(RenderSiteRuntimeConfig(tricky), &back); err != nil || back["X"] != tricky["X"] || len(back) != 1 {
		t.Fatalf("round trip = %v, %v", back, err)
	}
}
