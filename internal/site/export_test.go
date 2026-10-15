package site

import v1 "github.com/pyvvo/funcd/api/types/v1alpha1"

// WithBase, IndexCopyRel and BaseHrefOf expose the base copy's rules to the external tests (ADR-0239 Decision 10).
func WithBase(doc []byte, href string) []byte      { return withBase(doc, href) }
func IndexCopyRel(s *v1.Site, index string) string { return indexCopyRel(s, index) }
func BaseHrefOf(s *v1.Site) string                 { return baseHrefOf(s) }
