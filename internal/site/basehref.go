package site

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// runtimeDir is the root entry funcd reserves in every bundle for a Site's generated files (ADR-0239 Decision 10).
const runtimeDir = "_runtime"

func isRuntimeEntry(rel string) bool {
	return rel == runtimeDir || strings.HasPrefix(rel, runtimeDir+"/")
}

// indexCopyRel is where, inside a digest folder, a Site with baseHref keeps its index copy. The key names the Site and a
// hash of its mount, so a new mount gets a new key and switches with the Route.
func indexCopyRel(s *v1.Site, index string) string {
	sum := sha256.Sum256([]byte(s.EffectivePath()))
	return runtimeDir + "/" + string(s.Name) + "/" + hex.EncodeToString(sum[:6]) + "/" + index
}

// baseHrefOf is the base a Site's index copy carries: its mount ending with "/", percent-encoded as a URL path.
func baseHrefOf(s *v1.Site) string {
	p := s.EffectivePath()
	if p != "/" {
		p += "/"
	}
	return (&url.URL{Path: p}).EscapedPath()
}

// writeIndexCopy writes the copy of the bundle's index at sp+index, with the base, to sp+rel when it differs from the
// stored copy. A copy the Bucket refuses is a reason, like a bundle object it refuses.
func writeIndexCopy(ctx context.Context, view blob.Bucket, sp, index, rel, href string) (reason, message string, err error) {
	src, gerr := view.Get(ctx, sp+index)
	if gerr != nil {
		return "", "", fault.Wrapf(gerr, fault.KindOf(gerr), op, "get %q", sp+index)
	}
	body, key := withBase(src, href), sp+rel
	stored, serr := view.Get(ctx, key)
	switch {
	case serr == nil && bytes.Equal(stored, body):
		return "", "", nil
	case serr != nil && fault.KindOf(serr) != fault.NotFound:
		return "", "", fault.Wrapf(serr, fault.KindOf(serr), op, "get %q", key)
	}
	if perr := view.Put(ctx, key, body, blob.PutOptions{ContentType: "text/html; charset=utf-8"}); perr != nil {
		if k := fault.KindOf(perr); k == fault.PayloadTooLarge || k == fault.Invalid {
			return "MaterializeFailed", perr.Error(), nil
		}
		return "", "", fault.Wrapf(perr, fault.KindOf(perr), op, "put %q", key)
	}
	return "", "", nil
}

type span struct{ from, to int }

// withBase returns doc with exactly one <base href> (ADR-0239 Decision 10): every <base> is removed, and one, keeping
// the first one's target, goes first in <head> (after a <meta charset> that opens it), else right after <html>, else
// before the first element. Every other byte stays.
func withBase(doc []byte, href string) []byte {
	var (
		bases                     []span
		target                    string
		afterHead, afterHTML, at0 = -1, -1, -1
		openHead                  bool
	)
	z := html.NewTokenizer(bytes.NewReader(doc))
	for pos := 0; ; {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		start, end := pos, pos+len(z.Raw())
		pos = end
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		attrs := map[string]string{}
		for hasAttr {
			var k, v []byte
			k, v, hasAttr = z.TagAttr()
			attrs[string(k)] = string(v)
		}
		if at0 < 0 {
			at0 = start
		}
		switch tag := string(name); {
		case tag == "base":
			if len(bases) == 0 {
				target = attrs["target"]
			}
			bases = append(bases, span{start, end})
		case tag == "head" && afterHead < 0:
			afterHead, openHead = end, true
			continue
		case tag == "meta" && openHead:
			if _, ok := attrs["charset"]; ok {
				afterHead = end
			}
		case tag == "html" && afterHTML < 0:
			afterHTML = end
		}
		openHead = false
	}
	el := `<base href="` + html.EscapeString(href) + `"`
	if target != "" {
		el += ` target="` + html.EscapeString(target) + `"`
	}
	el += ">"
	insert := at0
	for _, at := range []int{afterHead, afterHTML} {
		if at >= 0 {
			insert = at
			break
		}
	}
	if insert < 0 {
		insert = 0
	}
	out := make([]byte, 0, len(doc)+len(el))
	prev := 0
	emit := func(to int) {
		for _, b := range bases {
			if b.from >= prev && b.to <= to {
				out = append(out, doc[prev:b.from]...)
				prev = b.to
			}
		}
		out = append(out, doc[prev:to]...)
		prev = to
	}
	emit(insert)
	out = append(out, el...)
	emit(len(doc))
	return out
}
