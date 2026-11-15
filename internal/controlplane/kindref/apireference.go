package kindref

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// SectionMeta is a guide section's title and icon, from docs/guide/develop/<section>/meta.json.
type SectionMeta struct{ Title, Icon string }

// The API reference's generated files, by their path from the repository root (ADR-0331 Decision 3.2).
const (
	pathsFile    = "api/openapi/reference-paths.json"
	referenceDir = "docs/guide/api"
)

// verbs are the operations that a kind's folder lists first, in this order; the others follow by name.
//
//nolint:gochecknoglobals // an immutable lookup table
var verbs = []string{"list", "create", "get", "replace", "delete"}

// wordStart is where a word of an operation ID starts: a capital after a lower-case letter.
var wordStart = regexp.MustCompile(`([a-z])([A-Z])`)

// referencePage is an operation's page of the API reference, at <section>/<its tag in lower case>/<name>.
type referencePage struct {
	id, section, tag, name string
}

func (p referencePage) path() string { return p.section + "/" + strings.ToLower(p.tag) + "/" + p.name }

// ReferencePaths returns the reference page of each described operation whose tag is not an Omitted kind, by
// operation ID: "<section>/<kind>/<name>", such as "services/kvstore/list". Paths are unique.
func ReferencePaths(doc *huma.OpenAPI) map[string]string {
	pages, _ := referencePages(operationsOf(doc))
	return pathsOf(pages)
}

// ReferenceFiles returns the reference's generated files by path from the repository root, unlike Render:
// api/openapi/reference-paths.json, the map that ReferencePaths returns; docs/guide/api/meta.json ({root, title,
// description, icon "SquareCode", pages: index and the sections}); one meta.json per section ({title, icon, pages: its
// kinds}); and one per kind ({title: the kind, pages: its operations in order}). specgen passes the guide's sections by
// directory, read from their meta.json files. It fails on an operation that no section holds, on two operations that
// would share a page, and on a section that sections lacks.
func ReferenceFiles(doc *huma.OpenAPI, sections map[string]SectionMeta) (map[string][]byte, error) {
	pages, unplaced := referencePages(operationsOf(doc))
	if len(unplaced) > 0 {
		return nil, fmt.Errorf("kindref: no section of the API reference holds %s: an operation's tag is a kind of "+
			"Features, or TagFeatures places it", strings.Join(unplaced, ", "))
	}
	byPath := map[string]string{}
	for _, p := range pages {
		if other, taken := byPath[p.path()]; taken {
			return nil, fmt.Errorf("kindref: %s and %s would share the reference page %s", other, p.id, p.path())
		}
		byPath[p.path()] = p.id
	}
	paths, _ := json.MarshalIndent(pathsOf(pages), "", "  ") // a map of strings always encodes
	files := map[string][]byte{pathsFile: append(paths, '\n')}
	root := meta{Root: true, Title: "API reference", Description: "The operations of funcd's control plane API",
		Icon: "SquareCode", Pages: []string{"index"}}
	for _, s := range referenceSections() {
		var folders []string
		for _, tag := range s.tags {
			var names []string
			for _, p := range pages {
				if p.section == s.name && p.tag == tag {
					names = append(names, p.name)
				}
			}
			if len(names) == 0 {
				continue
			}
			slices.SortFunc(names, byVerb)
			folder := strings.ToLower(tag)
			folders = append(folders, folder)
			files[referenceDir+"/"+s.name+"/"+folder+"/meta.json"] = metaFile(meta{Title: tag, Pages: names})
		}
		if len(folders) == 0 {
			continue
		}
		guide, ok := sections[s.name]
		if !ok {
			return nil, fmt.Errorf("kindref: the guide has no section %s, whose meta.json gives the reference's "+
				"section its title and icon", s.name)
		}
		files[referenceDir+"/"+s.name+"/meta.json"] = metaFile(meta{Title: guide.Title, Icon: guide.Icon, Pages: folders})
		root.Pages = append(root.Pages, s.name)
	}
	files[referenceDir+"/meta.json"] = metaFile(root)
	return files, nil
}

// meta is a folder's meta.json, which names the folder on the docs site and orders its pages.
type meta struct {
	Root        bool     `json:"root,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Pages       []string `json:"pages"`
}

func metaFile(m meta) []byte {
	b, _ := json.MarshalIndent(m, "", "  ") // a struct of strings always encodes
	return append(b, '\n')
}

func pathsOf(pages []referencePage) map[string]string {
	out := make(map[string]string, len(pages))
	for _, p := range pages {
		out[p.id] = p.path()
	}
	return out
}

// referencePages returns the page of each operation that the reference covers, a described one whose tag is not an
// Omitted kind, and the IDs of the covered operations that no section holds.
func referencePages(ops []operation) ([]referencePage, []string) {
	var pages []referencePage
	var unplaced []string
	for _, o := range ops {
		if !o.covered() {
			continue
		}
		section, ok := sectionOf(o.tag())
		if !ok {
			unplaced = append(unplaced, o.op.OperationID)
			continue
		}
		pages = append(pages, referencePage{id: o.op.OperationID, section: section, tag: o.tag(),
			name: pageName(o.op.OperationID, o.tag())})
	}
	return pages, unplaced
}

// covered reports whether the API reference shows the operation: it is described, and its tag is not an Omitted kind.
func (o operation) covered() bool {
	return o.described() && !slices.Contains(Omitted, v1.Kind(o.tag()))
}

// sectionOf returns the reference's section of the operations tagged tag: the first folder of its kind's feature, or
// of the feature that TagFeatures places it on.
func sectionOf(tag string) (string, bool) {
	for _, f := range Features {
		if slices.Contains(f.Kinds, v1.Kind(tag)) {
			return topFolder(f.Path), true
		}
	}
	if path, ok := TagFeatures[tag]; ok {
		return topFolder(path), true
	}
	return "", false
}

func topFolder(featurePath string) string {
	folder, _, _ := strings.Cut(featurePath, "/")
	return folder
}

// pageName is an operation's name in its kind's folder: its ID without its tag's plural, or else its tag, in kebab
// case, as listKVStores is list and getFunctionLogs get-logs. A tag that ends in y takes ies, any other es or s.
func pageName(id, tag string) string {
	forms := []string{tag + "es", tag + "s"}
	if stem, ok := strings.CutSuffix(tag, "y"); ok {
		forms = []string{stem + "ies"}
	}
	for _, form := range append(forms, tag) {
		if strings.Contains(id, form) {
			id = strings.Replace(id, form, "", 1)
			break
		}
	}
	return strings.ToLower(wordStart.ReplaceAllString(id, "$1-$2"))
}

// byVerb orders a kind's page names: list, create, get, replace and delete, then the others by name.
func byVerb(a, b string) int {
	rank := func(name string) int {
		if i := slices.Index(verbs, name); i >= 0 {
			return i
		}
		return len(verbs)
	}
	return cmp.Or(cmp.Compare(rank(a), rank(b)), strings.Compare(a, b))
}

// referenceSection is one section of the reference, with the tags whose folders it may hold, in its order.
type referenceSection struct {
	name string
	tags []string
}

// referenceSections lists the reference's sections in the order of Features, which is the guide's, each with its
// kinds in that order and then the tags that TagFeatures places in it, by name.
func referenceSections() []referenceSection {
	var out []referenceSection
	at := func(name string) int {
		i := slices.IndexFunc(out, func(s referenceSection) bool { return s.name == name })
		if i < 0 {
			out = append(out, referenceSection{name: name})
			i = len(out) - 1
		}
		return i
	}
	for _, f := range Features {
		i := at(topFolder(f.Path))
		for _, k := range f.Kinds {
			out[i].tags = append(out[i].tags, string(k))
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(TagFeatures)) {
		i := at(topFolder(TagFeatures[tag]))
		out[i].tags = append(out[i].tags, tag)
	}
	return out
}

// referenceFields records in fields the description of each field that the API reference shows and no Spec page
// does: those of the bodies and answers of the operations it covers, and of the objects they hold, but a kind's,
// which its Spec page shows, and the common definitions'.
func referenceFields(d document, fields map[string]string) {
	r := &renderer{schemas: d.schemas, fields: fields, anchors: map[string]string{}}
	for _, name := range common {
		r.anchors[name] = name
	}
	for _, f := range Features {
		for _, k := range f.Kinds {
			r.anchors[string(k)] = string(k)
		}
	}
	var nested []child
	queue := func(content map[string]*huma.MediaType, path string) {
		for _, ct := range slices.Sorted(maps.Keys(content)) {
			r.queue(content[ct].Schema, path, &nested)
		}
	}
	for _, o := range d.ops {
		if !o.covered() {
			continue
		}
		if b := o.op.RequestBody; b != nil {
			queue(b.Content, o.op.OperationID+".body")
		}
		for _, code := range slices.Sorted(maps.Keys(o.op.Responses)) {
			queue(o.op.Responses[code].Content, o.op.OperationID+"."+code)
		}
	}
	for _, c := range nested {
		r.section("`"+c.path+"`", 4, c.schema, c.path, c.owner)
	}
}

// queue queues the table of s, a body or an answer at path, as a Spec page would show it: an object's own, or its
// items'.
func (r *renderer) queue(s *huma.Schema, path string, nested *[]child) {
	if s == nil {
		return
	}
	name, target := r.resolve(s)
	switch {
	case target.Type == huma.TypeArray && target.Items != nil:
		r.queue(target.Items, path+"[]", nested)
	case len(target.Properties) > 0:
		r.link(name, path, path, target, nested)
	}
}
