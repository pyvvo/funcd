package plan

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

const editOp = "plan.Apply"

// Apply sets each of s in doc (Decision 4), every other byte kept: a present scalar's text is replaced in place, a
// missing key (or parent) is inserted after its parent mapping's last line at the indentation of that mapping's first
// child (top level: at the end of the document, before the next document marker). kind "" ⇒ doc is a config file of
// at most one document; else the key is set in the one document of that kind. A key under an alias, an anchor, a
// block scalar or a flow collection, or a config file of several documents, is fault.Invalid naming the key. A key
// already at its value is no Change.
func Apply(doc []byte, kind v1.Kind, s []Setting) ([]byte, []Change, error) {
	out := bytes.Clone(doc)
	var changes []Change
	for _, st := range s {
		next, ch, err := set(out, kind, st)
		if err != nil {
			return nil, nil, err
		}
		if ch != nil {
			out = next
			changes = append(changes, *ch)
		}
	}
	return out, changes, nil
}

// set applies one Setting to src, re-parsed each time so every edit sees the bytes the previous one left.
func set(src []byte, kind v1.Kind, st Setting) ([]byte, *Change, error) {
	root, err := documentRoot(src, kind, st.Key)
	if err != nil {
		return nil, nil, err
	}
	path := strings.Split(st.Key, ".")
	value := render(st.Value)
	added := &Change{Key: st.Key, New: st.Value}
	if root == nil {
		return insert(src, len(src), block(path, 0, defaultStep, value)), added, nil
	}
	m, keyIndent := root, -1
	for i, k := range path {
		if err := editableMapping(m, st.Key); err != nil {
			return nil, nil, err
		}
		key, v := lookup(m, k)
		if v == nil {
			c, step := m.Content[0].Column-1, stepUnder(m, keyIndent, root)
			at := documentEnd(src, m.Content[0].Line)
			if m != root {
				at = lineEnd(src, lastLine(src, m.Content[0].Line, c))
			}
			return insert(src, at, block(path[i:], c, step, value)), added, nil
		}
		if v.Kind == yamlv3.AliasNode || v.Anchor != "" || key.Anchor != "" {
			return nil, nil, refuse(st.Key, "it sits under an alias or an anchor")
		}
		if i == len(path)-1 {
			return replace(src, v, st)
		}
		switch {
		case v.Kind == yamlv3.MappingNode:
			m, keyIndent = v, key.Column-1
		case v.Kind == yamlv3.ScalarNode && v.ShortTag() == "!!null" && v.Value == "":
			step := stepUnder(m, keyIndent, root)
			at := lineEnd(src, key.Line)
			return insert(src, at, block(path[i+1:], key.Column-1+step, step, value)), added, nil
		default:
			return nil, nil, refuse(st.Key, strings.Join(path[:i+1], ".")+" is not a mapping")
		}
	}
	return nil, nil, refuse(st.Key, "an empty key")
}

// documentRoot is the root mapping the key is set in: the config file's only document (nil when it holds none), or
// the one document of kind.
func documentRoot(src []byte, kind v1.Kind, key string) (*yamlv3.Node, error) {
	dec := yamlv3.NewDecoder(bytes.NewReader(src))
	var roots []*yamlv3.Node
	for {
		var doc yamlv3.Node
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fault.Invalidf(editOp, "cannot edit %s: parse the file: %v", key, err)
		}
		if len(doc.Content) == 0 || doc.Content[0].ShortTag() == "!!null" {
			continue
		}
		roots = append(roots, doc.Content[0])
	}
	if kind == "" {
		switch {
		case len(roots) > 1:
			return nil, refuse(key, "the config file holds "+strconv.Itoa(len(roots))+" documents")
		case len(roots) == 0:
			return nil, nil
		}
		return roots[0], nil
	}
	var match []*yamlv3.Node
	for _, r := range roots {
		if _, v := lookup(r, "kind"); r.Kind == yamlv3.MappingNode && v != nil && v.Value == string(kind) {
			match = append(match, r)
		}
	}
	if len(match) != 1 {
		return nil, refuse(key, "the manifest holds "+strconv.Itoa(len(match))+" "+string(kind)+" documents: want one")
	}
	return match[0], nil
}

// editableMapping refuses a mapping plan cannot splice: not a mapping, a flow collection, an anchor or a merge key.
func editableMapping(m *yamlv3.Node, key string) error {
	switch {
	case m.Kind != yamlv3.MappingNode || len(m.Content) == 0:
		return refuse(key, "its parent is not a mapping")
	case m.Style&yamlv3.FlowStyle != 0:
		return refuse(key, "it sits in a flow collection")
	case m.Anchor != "":
		return refuse(key, "it sits under an anchor")
	}
	if k, _ := lookup(m, "<<"); k != nil {
		return refuse(key, "it sits under an alias (a << merge key)")
	}
	return nil
}

// replace swaps a present scalar's text for st's value.
func replace(src []byte, v *yamlv3.Node, st Setting) ([]byte, *Change, error) {
	switch {
	case v.Kind != yamlv3.ScalarNode:
		return nil, nil, refuse(st.Key, "it is not a scalar")
	case v.Style&(yamlv3.LiteralStyle|yamlv3.FoldedStyle) != 0:
		return nil, nil, refuse(st.Key, "it is a block scalar")
	case v.Style&yamlv3.TaggedStyle != 0:
		return nil, nil, refuse(st.Key, "it carries a tag")
	case v.ShortTag() == "!!null" && v.Value == "":
		return nil, nil, refuse(st.Key, "it has no value")
	case v.Value == st.Value:
		return src, nil, nil
	}
	start, ok := offset(src, v.Line, v.Column)
	n := -1
	if ok {
		n = tokenLen(src[start:lineEnd(src, v.Line)], v)
	}
	if n <= 0 {
		return nil, nil, refuse(st.Key, "its value spans several lines")
	}
	out := make([]byte, 0, len(src)+len(st.Value)+2)
	out = append(append(append(out, src[:start]...), render(st.Value)...), src[start+n:]...)
	return out, &Change{Key: st.Key, Old: v.Value, New: st.Value}, nil
}

// tokenLen is the byte length of v's token at the start of line, or -1 when it does not end on that line.
func tokenLen(line []byte, v *yamlv3.Node) int {
	switch {
	case v.Style&yamlv3.DoubleQuotedStyle != 0:
		for i := 1; i < len(line); i++ {
			switch line[i] {
			case '\\':
				i++
			case '"':
				return i + 1
			}
		}
	case v.Style&yamlv3.SingleQuotedStyle != 0:
		for i := 1; i < len(line); i++ {
			if line[i] == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				return i + 1
			}
		}
	case bytes.HasPrefix(line, []byte(v.Value)):
		rest := line[len(v.Value):]
		if len(bytes.TrimSpace(rest)) == 0 || (rest[0] == ' ' || rest[0] == '\t') && bytes.TrimSpace(rest)[0] == '#' {
			return len(v.Value)
		}
	}
	return -1
}

// lookup is the key node and value node of k in mapping m, or nils.
func lookup(m *yamlv3.Node, k string) (*yamlv3.Node, *yamlv3.Node) {
	for i := 0; m.Kind == yamlv3.MappingNode && i+1 < len(m.Content); i += 2 {
		if key := m.Content[i]; key.Kind == yamlv3.ScalarNode && key.Value == k {
			return key, m.Content[i+1]
		}
	}
	return nil, nil
}

// defaultStep is the indentation step of keys inserted where the file shows none.
const defaultStep = 2

// stepUnder is the indentation step the file uses below m: its children's indentation less its key's, or for the root
// mapping that of its first block mapping value.
func stepUnder(m *yamlv3.Node, keyIndent int, root *yamlv3.Node) int {
	if keyIndent >= 0 {
		if s := m.Content[0].Column - 1 - keyIndent; s > 0 {
			return s
		}
		return defaultStep
	}
	for i := 1; i < len(root.Content); i += 2 {
		v := root.Content[i]
		if v.Kind == yamlv3.MappingNode && v.Style&yamlv3.FlowStyle == 0 && len(v.Content) > 0 {
			if s := v.Content[0].Column - root.Content[i-1].Column; s > 0 {
				return s
			}
		}
	}
	return defaultStep
}

// block renders the keys path, nested from indentation c by step, the last one holding value.
func block(path []string, c, step int, value string) []byte {
	var b strings.Builder
	for j, k := range path {
		b.WriteString(strings.Repeat(" ", c+j*step))
		b.WriteString(k)
		if j == len(path)-1 {
			b.WriteString(": " + value + "\n")
		} else {
			b.WriteString(":\n")
		}
	}
	return []byte(b.String())
}

// render is v as a plain scalar when it reads back as v, else double-quoted (`*/30 * * * *`).
func render(v string) string {
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal([]byte("k: "+v), &doc); err == nil && len(doc.Content) == 1 {
		if _, s := lookup(doc.Content[0], "k"); s != nil && s.Kind == yamlv3.ScalarNode && s.Style == 0 && s.Value == v {
			return v
		}
	}
	return strconv.Quote(v)
}

// insert puts text at byte offset at, after a line break the bytes before it lack.
func insert(src []byte, at int, text []byte) []byte {
	out := make([]byte, 0, len(src)+len(text)+1)
	out = append(out, src[:at]...)
	if at > 0 && src[at-1] != '\n' {
		out = append(out, '\n')
	}
	return append(append(out, text...), src[at:]...)
}

// lastLine is the last line (1-based) of the block mapping whose first key is on line from at indentation c: the
// last line from there, blank and comment lines aside, indented at least c, before a document marker.
func lastLine(src []byte, from, c int) int {
	last := from
	for i, l := range lines(src) {
		if i+1 < from {
			continue
		}
		t := bytes.TrimSpace(l)
		if len(t) == 0 || t[0] == '#' {
			continue
		}
		if docMarker(l) || indent(l) < c {
			break
		}
		last = i + 1
	}
	return last
}

// documentEnd is the byte offset of the first document marker after line from (1-based), else len(src).
func documentEnd(src []byte, from int) int {
	off := 0
	for i, l := range lines(src) {
		if i+1 > from && docMarker(l) {
			return off
		}
		off += len(l) + 1
	}
	return len(src)
}

// lineEnd is the byte offset just past line n (1-based) and its line break; len(src) past the last line.
func lineEnd(src []byte, n int) int {
	off := 0
	for i, l := range lines(src) {
		off += len(l)
		if i+1 == n {
			if off < len(src) {
				off++
			}
			return off
		}
		off++
	}
	return len(src)
}

// offset is the byte offset of 1-based line and column, yaml.v3 counting the column in characters.
func offset(src []byte, line, col int) (int, bool) {
	off := 0
	for i, l := range lines(src) {
		if i+1 == line {
			for c := 1; c < col; c++ {
				if len(l) == 0 {
					return 0, false
				}
				_, w := utf8.DecodeRune(l)
				l, off = l[w:], off+w
			}
			return off, true
		}
		off += len(l) + 1
	}
	return 0, false
}

// lines splits src at its line breaks, without them.
func lines(src []byte) [][]byte { return bytes.Split(src, []byte("\n")) }

// docMarker reports a "---" or "..." document marker line.
func docMarker(l []byte) bool {
	for _, m := range []string{"---", "..."} {
		if rest, ok := bytes.CutPrefix(l, []byte(m)); ok && (len(rest) == 0 || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\r') {
			return true
		}
	}
	return false
}

// indent is l's count of leading spaces.
func indent(l []byte) int { return len(l) - len(bytes.TrimLeft(l, " ")) }

// refuse is the fault.Invalid of a key plan cannot edit.
func refuse(key, why string) error {
	return fault.Invalidf(editOp, "cannot edit %s: %s; edit the file by hand", key, why)
}
