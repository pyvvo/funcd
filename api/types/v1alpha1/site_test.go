package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

func site(mutate func(*Site)) *Site {
	s := &Site{
		TypeMeta:   TypeMeta{APIVersion: KindSite.GVK().APIVersion(), Kind: KindSite},
		ObjectMeta: ObjectMeta{Name: "bi", Namespace: "default", ResourceGroup: "rg1"},
		Spec: SiteSpec{
			Image:   "oci-layout:///tmp/layout:bi",
			Bucket:  SiteBucket{Name: "reports"},
			Prefix:  "bi",
			Ingress: SiteIngress{Host: "bi.example.com", Rules: []SiteRule{{Path: "/data", Prefix: "gold"}}},
		},
	}
	if mutate != nil {
		mutate(s)
	}
	return s
}

// scenario (types): Site roundtrip — the spec (bucket, ingress rules, auth) and the derived status
// survive JSON marshal/unmarshal (ADR-0139).
func TestSiteRoundtrip(t *testing.T) {
	s := site(func(s *Site) {
		s.Spec.SPA = true
		s.Spec.Ingress.Auth = &RouteAuth{Mode: AuthOpen}
		s.Spec.Bucket.Prefixes = []BucketPrefix{{Name: "gold", Owner: "etl"}}
		s.Status.Digest = "sha256:" + strings.Repeat("a", 64)
		s.Status.ServingPrefix = "bi/sha256-" + strings.Repeat("a", 64) + "/"
	})
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal Site: %v", err)
	}
	var s2 Site
	if err := json.Unmarshal(data, &s2); err != nil {
		t.Fatalf("unmarshal Site: %v", err)
	}
	if s2.Spec.Image != s.Spec.Image || s2.Spec.Prefix != "bi" || !s2.Spec.SPA || s2.Spec.Bucket.Name != "reports" {
		t.Errorf("Site spec roundtrip mismatch: %+v", s2.Spec)
	}
	if len(s2.Spec.Ingress.Rules) != 1 || s2.Spec.Ingress.Rules[0].Path != "/data" || s2.Spec.Ingress.Auth == nil || s2.Spec.Ingress.Auth.Mode != AuthOpen {
		t.Errorf("Site ingress roundtrip mismatch: %+v", s2.Spec.Ingress)
	}
	if s2.Status.Digest != s.Status.Digest || s2.Status.ServingPrefix != s.Status.ServingPrefix {
		t.Errorf("Site status roundtrip mismatch: %+v", s2.Status)
	}
	if got, ok := NewObject(KindSite); !ok || got.GroupVersionKind() != KindSite.GVK() {
		t.Errorf("NewObject(KindSite) = %v, %v", got, ok)
	}
}

// scenario: delete-cascade-rejected + public-and-authenticated-rejected + the prefix/index/rule rules
// (ADR-0139) — the Site.Validate matrix: a sound Site validates; every semantic rule rejects with
// fault.Invalid.
func TestSiteValidate(t *testing.T) {
	if err := site(nil).Validate(); err != nil {
		t.Fatalf("valid Site rejected: %v", err)
	}
	if err := site(func(s *Site) { s.Spec.Bucket.Deletion = DeletionRetain; s.Spec.Ingress = SiteIngress{} }).Validate(); err != nil {
		t.Fatalf("retain + a zero ingress (any host, bundle only) must validate: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Site)
		want   string // a substring of the Invalid message
	}{
		{"delete-cascade-rejected", func(s *Site) { s.Spec.Bucket.Deletion = DeletionDelete }, "not implemented in V1"},
		{"unknown deletion", func(s *Site) { s.Spec.Bucket.Deletion = "purge" }, "spec.bucket.deletion"},
		{"public-and-authenticated-rejected", func(s *Site) {
			s.Spec.Ingress.Public = true
			s.Spec.Ingress.Auth = &RouteAuth{Mode: AuthAuthenticated}
		}, "public must not override"},
		{"bad auth mode", func(s *Site) { s.Spec.Ingress.Auth = &RouteAuth{Mode: "sometimes"} }, "auth.mode"},
		{"missing image", func(s *Site) { s.Spec.Image = "" }, "spec.image"},
		{"bad prefix", func(s *Site) { s.Spec.Prefix = "Bad_Prefix" }, "spec.prefix"},
		{"absolute index", func(s *Site) { s.Spec.Index = "/index.html" }, "spec.index"},
		{"bad bucket name", func(s *Site) { s.Spec.Bucket.Name = "Bad_Bucket" }, "spec.bucket.name"},
		{"prefixes repeat spec.prefix", func(s *Site) { s.Spec.Bucket.Prefixes = []BucketPrefix{{Name: "bi"}} }, "must not repeat spec.prefix"},
		{"duplicate declared prefix", func(s *Site) { s.Spec.Bucket.Prefixes = []BucketPrefix{{Name: "gold"}, {Name: "gold"}} }, "duplicated"},
		{"bad declared prefix", func(s *Site) { s.Spec.Bucket.Prefixes = []BucketPrefix{{Name: "Gold!"}} }, "spec.bucket.prefixes"},
		{"rule path without slash", func(s *Site) { s.Spec.Ingress.Rules = []SiteRule{{Path: "data", Prefix: "gold"}} }, "must begin with '/'"},
		{"rule path is root", func(s *Site) { s.Spec.Ingress.Rules = []SiteRule{{Path: "/", Prefix: "gold"}} }, "must not be \"/\""},
		{"duplicate rule path", func(s *Site) {
			s.Spec.Ingress.Rules = []SiteRule{{Path: "/data", Prefix: "gold"}, {Path: "/data", Prefix: "silver"}}
		}, "duplicates an earlier rule"},
		{"bad rule prefix", func(s *Site) { s.Spec.Ingress.Rules = []SiteRule{{Path: "/data", Prefix: "Gold!"}} }, "rules[0].prefix"},
		{"bad rule bucket", func(s *Site) { s.Spec.Ingress.Rules = []SiteRule{{Path: "/data", Prefix: "gold", Bucket: "Bad!"}} }, "rules[0].bucket"},
		{"wrong kind", func(s *Site) { s.Kind = KindRoute }, "kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := site(tc.mutate).Validate()
			if fault.KindOf(err) != fault.Invalid {
				t.Fatalf("want Invalid, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("message %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// scenario: (ADR-0140) EffectivePath resolution — an explicit path wins; else "/" with a host; else the
// by-name default "/site/<name>" so several host-less Sites share one listener.
func TestSiteEffectivePath(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Site)
		want   string
	}{
		{"explicit path wins over a host", func(s *Site) { s.Spec.Ingress.Path = "/bi" }, "/bi"},
		{"explicit root", func(s *Site) { s.Spec.Ingress.Host = ""; s.Spec.Ingress.Path = "/" }, "/"},
		{"host, no path ⇒ root (ADR-0139 behaviour)", nil, "/"},
		{"no host, no path ⇒ by-name", func(s *Site) { s.Spec.Ingress.Host = "" }, "/site/bi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := site(tc.mutate).EffectivePath(); got != tc.want {
				t.Fatalf("EffectivePath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// scenario: path-collision-rejected + derived-path-collision-rejected (ADR-0140) — the path shape rules,
// and a rule colliding with the bundle mount is Invalid whether the mount was WRITTEN or DERIVED (the
// derived case is the one that would otherwise reach the reconciler as an unmapped store error).
func TestSiteValidatePath(t *testing.T) {
	for _, ok := range []struct {
		name   string
		mutate func(*Site)
	}{
		{"explicit mount, non-colliding rule", func(s *Site) { s.Spec.Ingress.Path = "/bi" }},
		{"mount nested above a data rule", func(s *Site) {
			s.Spec.Ingress.Path = "/bi"
			s.Spec.Ingress.Rules = []SiteRule{{Path: "/bi/data", Prefix: "gold"}}
		}},
		{"root mount", func(s *Site) { s.Spec.Ingress.Path = "/" }},
		{"host-less by-name mount", func(s *Site) { s.Spec.Ingress.Host = "" }},
	} {
		t.Run("valid/"+ok.name, func(t *testing.T) {
			if err := site(ok.mutate).Validate(); err != nil {
				t.Fatalf("valid Site rejected: %v", err)
			}
		})
	}
	bad := []struct {
		name   string
		mutate func(*Site)
		want   string
	}{
		{"no leading slash", func(s *Site) { s.Spec.Ingress.Path = "bi" }, "must begin with '/'"},
		{"trailing slash", func(s *Site) { s.Spec.Ingress.Path = "/bi/" }, "must not end with '/'"},
		{"path-collision-rejected (written mount)", func(s *Site) {
			s.Spec.Ingress.Path = "/data"
			s.Spec.Ingress.Rules = []SiteRule{{Path: "/data", Prefix: "gold"}}
		}, "collides with the bundle mount"},
		{"derived-path-collision-rejected (host-less default)", func(s *Site) {
			s.Spec.Ingress.Host = "" // ⇒ the mount derives to /site/bi
			s.Spec.Ingress.Rules = []SiteRule{{Path: "/site/bi", Prefix: "gold"}}
		}, "collides with the bundle mount"},
		{"derived-path-collision-rejected (hosted default)", func(s *Site) {
			s.Spec.Ingress.Rules = []SiteRule{{Path: "/", Prefix: "gold"}} // "/" is the derived mount
		}, "must not be \"/\""},
	}
	for _, tc := range bad {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			err := site(tc.mutate).Validate()
			if fault.KindOf(err) != fault.Invalid {
				t.Fatalf("want Invalid, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("message %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}
