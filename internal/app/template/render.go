package template

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/expr"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const renderOp = "app.render"

// The roots an expression of a template reads (Decision 5).
const (
	valuesRoot = "values"
	appRoot    = "app"
	imagesRoot = "images"
)

// RenderInput is what an install gives a template: the App's name, namespace and resource group, and the values.
type RenderInput struct {
	Name          v1.ObjectName        // empty ⇒ Template.Name
	Namespace     v1.NamespaceName     // empty ⇒ "default"
	ResourceGroup v1.ResourceGroupName // empty ⇒ Name
	Values        []json.RawMessage    // the -f files in order, each from ReadValues
}

const appSchema = `{"type":"object","required":["name","namespace","version"],"properties":{` +
	`"name":{"type":"string"},"namespace":{"type":"string"},"version":{"type":"string"}}}`

// The image and digest paths match case-insensitively, because the strict decode (encoding/json) matches a key to a
// field without regard to case: a key in any letter case that decodes into one of these fields is routed.
var (
	imagePath  = regexp.MustCompile(`(?i)^(functions\[\d+\]|workflows\[\d+\]\.steps\[\d+\]\.function|sites\[\d+\])\.image$`)
	digestPath = regexp.MustCompile(`(?i)^functions\[\d+\]\.imageDigest$`)
	imageRef   = regexp.MustCompile(`^\s*\$\{\{\s*images\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}\s*$`)
	specEntry  = regexp.MustCompile(`spec\.((?:hooks\.)?[A-Za-z]+)\[(\d+)\]`)
)

// Render renders the template into one App (ADR-0217 Decisions 3-7): it merges and validates the values, evaluates
// registry and every when, routes the scalars of each included fragment, decodes each fragment strictly, appends
// their sections in file order and runs App.Validate. Each image is the pinned value of its lock entry (ADR-0218
// Decision 4), so a template without a lock or with a stale one is refused. It returns nothing unless every step
// succeeded.
func Render(t *Template, in RenderInput) (*v1.App, error) {
	name := cmp.Or(in.Name, t.Name)
	ns := cmp.Or(in.Namespace, v1.NamespaceName("default"))
	if err := CheckLock(t); err != nil {
		return nil, err
	}
	values, err := mergeValues(in.Values)
	if err != nil {
		return nil, err
	}
	registry, err := evalRegistry(t, values, false)
	if err != nil {
		return nil, err
	}
	appDoc, err := json.Marshal(map[string]string{"name": string(name), "namespace": string(ns), "version": t.Version})
	if err != nil {
		return nil, fault.Internalf(renderOp, "marshal the app document: %v", err)
	}
	r := &router{
		docs:    map[string]json.RawMessage{valuesRoot: values, appRoot: appDoc},
		schemas: map[string]json.RawMessage{valuesRoot: t.ValuesSchema, appRoot: json.RawMessage(appSchema)},
	}
	if err := r.renderImages(t, registry); err != nil {
		return nil, err
	}
	app := &v1.App{
		TypeMeta: v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns,
			ResourceGroup: cmp.Or(in.ResourceGroup, v1.ResourceGroupName(name))},
	}
	origins := map[string][]origin{}
	for _, f := range slices.Sorted(maps.Keys(t.Files)) {
		included, err := r.included(t, f)
		if err != nil {
			return nil, err
		}
		if !included {
			continue
		}
		spec, err := r.fragment(f, t.Files[f])
		if err != nil {
			return nil, err
		}
		appendSections(&app.Spec, spec, f, origins)
	}
	app.Spec.Version = t.Version
	if err := app.Validate(); err != nil {
		return nil, fault.Invalidf(renderOp, "%s", relocate(err.Error(), origins))
	}
	return app, nil
}

// router evaluates the expressions of a template over its documents: values, app and, once rendered, images.
type router struct {
	docs    map[string]json.RawMessage
	schemas map[string]json.RawMessage
	images  map[string]string // name → <registry>/<repo>:<version>@<digest>
}

// resolver types the given roots strictly from their schemas (Decision 4).
func (r *router) resolver(roots ...string) expr.Resolver {
	schemas := make(map[string]json.RawMessage, len(roots))
	for _, root := range roots {
		schemas[root] = r.schemas[root]
	}
	return expr.NewSchemaResolver(schemas, nil, expr.StrictTypes())
}

// renderImages renders each image as its pinned value, <registry>/<repo>:<version>@<digest>, from the lock.
func (r *router) renderImages(t *Template, registry string) error {
	r.images = make(map[string]string, len(t.Images))
	props := map[string]json.RawMessage{}
	for name := range t.Images {
		img, err := t.parsed(name)
		if err != nil {
			return fault.Wrapf(err, fault.Invalid, renderOp, "app.yaml: images.%s", name)
		}
		if r.images[name], err = ImageRef(registry, img.Repo, t.Lock[name]); err != nil {
			return err
		}
		props[name] = json.RawMessage(`{"type":"string"}`)
	}
	doc, err := json.Marshal(r.images)
	if err != nil {
		return fault.Internalf(renderOp, "marshal the images document: %v", err)
	}
	schema, err := json.Marshal(map[string]interface{}{"type": "object", "properties": props, "required": slices.Sorted(maps.Keys(props))})
	if err != nil {
		return fault.Internalf(renderOp, "marshal the images schema: %v", err)
	}
	r.docs[imagesRoot], r.schemas[imagesRoot] = doc, schema
	return nil
}

// included evaluates the file's when, if any, over values and app.
func (r *router) included(t *Template, file string) (bool, error) {
	cond, ok := t.When[file]
	if !ok {
		return true, nil
	}
	e, err := expr.Parse(cond, expr.Condition)
	if err != nil {
		return false, fault.Invalidf(renderOp, "app.yaml: when.%s: %v", file, err)
	}
	if err := e.Check(r.resolver(valuesRoot, appRoot)); err != nil {
		return false, fault.Invalidf(renderOp, "app.yaml: when.%s: %v", file, err)
	}
	ok, err = e.EvalBool(r.docs)
	if err != nil {
		return false, fault.Invalidf(renderOp, "app.yaml: when.%s: %v", file, err)
	}
	return ok, nil
}

// fragment parses one resource file, routes its scalars and decodes it alone as the spec of an App.
func (r *router) fragment(file string, data []byte) (v1.AppSpec, error) {
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	var docs []*yamlv3.Node
	for {
		var doc yamlv3.Node
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return v1.AppSpec{}, fault.Invalidf(renderOp, "%s: %v", file, err)
		}
		docs = append(docs, &doc)
	}
	if len(docs) > 1 {
		return v1.AppSpec{}, fault.Invalidf(renderOp, "%s holds %d YAML documents, want one", file, len(docs))
	}
	if len(docs) == 0 || len(docs[0].Content) == 0 {
		return v1.AppSpec{}, nil
	}
	top := docs[0].Content[0]
	if top.Kind != yamlv3.MappingNode {
		return v1.AppSpec{}, fault.Invalidf(renderOp, "%s: the top level is not a mapping of App sections", file)
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		switch k := top.Content[i]; {
		case strings.EqualFold(k.Value, "version"):
			return v1.AppSpec{}, fault.Invalidf(renderOp, "%s:%d: %s: a fragment cannot set it; app.yaml's version is spec.version", file, k.Line, k.Value)
		case strings.EqualFold(k.Value, "paused"):
			return v1.AppSpec{}, fault.Invalidf(renderOp, "%s:%d: %s: a fragment cannot set it; funcdctl app pause and resume do (ADR-0212)", file, k.Line, k.Value)
		}
	}
	if err := r.walk(file, top, ""); err != nil {
		return v1.AppSpec{}, err
	}
	return decodeFragment(file, top)
}

// walk routes every scalar value under n (never a key); path is the field path from the fragment's root.
func (r *router) walk(file string, n *yamlv3.Node, path string) error {
	switch n.Kind {
	case yamlv3.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yamlv3.AliasNode || k.ShortTag() == "!!merge" {
				return refuse(file, k.Line, path, "a YAML alias or merge key is not allowed in a fragment, so every field is routed where it is written")
			}
			if k.ShortTag() == "!!binary" {
				return refuse(file, k.Line, path, "a !!binary key is not allowed in a fragment: the decode reads its decoded text, so routing would not see the field")
			}
			p := k.Value
			if path != "" {
				p = path + "." + k.Value
			}
			if err := r.walk(file, n.Content[i+1], p); err != nil {
				return err
			}
		}
	case yamlv3.SequenceNode:
		for i, c := range n.Content {
			if err := r.walk(file, c, path+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
	case yamlv3.AliasNode:
		return refuse(file, n.Line, path, "a YAML alias or merge key is not allowed in a fragment, so every field is routed where it is written")
	case yamlv3.ScalarNode:
		return r.scalar(file, n, path)
	}
	return nil
}

// scalar routes one scalar value (Decisions 5 and 6).
func (r *router) scalar(file string, n *yamlv3.Node, path string) error {
	switch {
	case imagePath.MatchString(path):
		m := imageRef.FindStringSubmatch(n.Value)
		if m == nil {
			return refuse(file, n.Line, path, "an image is exactly ${{ images.<name> }}, a name app.yaml's images declares")
		}
		ref, ok := r.images[m[1]]
		if !ok {
			return refuse(file, n.Line, path, fmt.Sprintf("images.%s is not declared in app.yaml", m[1]))
		}
		setString(n, ref)
		return nil
	case digestPath.MatchString(path):
		return refuse(file, n.Line, path, "a digest comes only through images and app.lock (ADR-0218 Decision 4)")
	}
	if !isExpression(n.Value) {
		if interpolates(n.Value) {
			return refuse(file, n.Line, path, "text around a ${{ }} over values, app or images is interpolation, which ADR-0095 leaves out: write one expression")
		}
		return nil
	}
	e, err := expr.Parse(n.Value, expr.Select)
	if err != nil {
		return refuse(file, n.Line, path, err.Error())
	}
	var own, other []string
	for _, id := range e.Idents() {
		if id == valuesRoot || id == appRoot || id == imagesRoot {
			own = append(own, id)
		} else {
			other = append(other, id)
		}
	}
	switch {
	case len(own) == 0:
		return nil
	case len(other) > 0:
		return refuse(file, n.Line, path, fmt.Sprintf("the expression reads %s and %s: render cannot evaluate it and the server cannot read values, app or images",
			strings.Join(own, ", "), strings.Join(other, ", ")))
	}
	if err := e.Check(r.resolver(valuesRoot, appRoot, imagesRoot)); err != nil {
		return refuse(file, n.Line, path, err.Error())
	}
	out, err := e.Eval(r.docs)
	if err != nil {
		return refuse(file, n.Line, path, err.Error())
	}
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(out, &doc); err != nil || len(doc.Content) != 1 {
		return fault.Internalf(renderOp, "%s:%d: %s: decode the result %s", file, n.Line, path, out)
	}
	if holdsExpression(doc.Content[0]) {
		return refuse(file, n.Line, path, fmt.Sprintf("%s gives %s: render never writes an expression; write it literally in the fragment", n.Value, out))
	}
	if p := imageField(doc.Content[0], path); p != "" {
		return refuse(file, n.Line, path, fmt.Sprintf("%s sets %s from a value: an image is exactly ${{ images.<name> }} written in the fragment, "+
			"and a digest comes only through images and app.lock (ADR-0218 Decision 4)", n.Value, p))
	}
	*n = *doc.Content[0]
	return nil
}

// imageField returns the path of the first image or digest field in n, a value substituted at path, or "" if none.
func imageField(n *yamlv3.Node, path string) string {
	if imagePath.MatchString(path) || digestPath.MatchString(path) {
		return path
	}
	p := ""
	switch n.Kind {
	case yamlv3.MappingNode:
		for i := 0; p == "" && i+1 < len(n.Content); i += 2 {
			p = imageField(n.Content[i+1], path+"."+n.Content[i].Value)
		}
	case yamlv3.SequenceNode:
		for i := 0; p == "" && i < len(n.Content); i++ {
			p = imageField(n.Content[i], path+"["+strconv.Itoa(i)+"]")
		}
	}
	return p
}

// holdsExpression reports a scalar in n, at any depth, that the server would read as an expression or that
// interpolates values, app or images (Decision 5: render never writes an expression).
func holdsExpression(n *yamlv3.Node) bool {
	if n.Kind == yamlv3.ScalarNode {
		return isExpression(n.Value) || interpolates(n.Value)
	}
	return slices.ContainsFunc(n.Content, holdsExpression)
}

func refuse(file string, line int, path, msg string) error {
	return fault.Invalidf(renderOp, "%s:%d: %s: %s", file, line, path, msg)
}

func setString(n *yamlv3.Node, s string) {
	n.Kind, n.Tag, n.Style, n.Value = yamlv3.ScalarNode, "!!str", yamlv3.DoubleQuotedStyle, s
}

// isExpression reports a scalar the server would read as an expression: its trimmed text starts with ${{.
func isExpression(text string) bool { return strings.HasPrefix(strings.TrimSpace(text), "${{") }

// interpolates reports text with a ${{ whose first identifier is values, app or images.
func interpolates(text string) bool {
	for rest := text; ; {
		i := strings.Index(rest, "${{")
		if i < 0 {
			return false
		}
		rest = strings.TrimLeft(rest[i+3:], " \t\r\n")
		end := strings.IndexFunc(rest, func(c rune) bool { return !identRune(c) })
		if end < 0 {
			end = len(rest)
		}
		switch rest[:end] {
		case valuesRoot, appRoot, imagesRoot:
			return true
		}
	}
}

func identRune(c rune) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// checkForm checks a value of app.yaml before any value is known: a literal (registry only) or one ${{ }} whose
// roots are among roots (Decision 2).
func checkForm(field, text string, mode expr.Mode, roots ...string) error {
	if !isExpression(text) {
		if mode == expr.Condition {
			return fault.Invalidf(loadOp, "%s is not one ${{ }} condition", field)
		}
		if interpolates(text) {
			return fault.Invalidf(loadOp, "%s is a literal or one ${{ }}, not interpolation", field)
		}
		return nil
	}
	e, err := expr.Parse(text, mode)
	if err != nil {
		return fault.Invalidf(loadOp, "%s: %v", field, err)
	}
	for _, id := range e.Idents() {
		if !slices.Contains(roots, id) {
			return fault.Invalidf(loadOp, "%s reads %s: it may read only %s", field, id, strings.Join(roots, " and "))
		}
	}
	return nil
}

// decodeFragment decodes a routed fragment alone as the spec of an App, strictly (ADR-0108), as funcdctl apply does.
func decodeFragment(file string, spec *yamlv3.Node) (v1.AppSpec, error) {
	str := func(s string) *yamlv3.Node { return &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: s} }
	doc := &yamlv3.Node{Kind: yamlv3.MappingNode, Content: []*yamlv3.Node{
		str("apiVersion"), str(v1.KindApp.GVK().APIVersion()), str("kind"), str(string(v1.KindApp)),
		str("metadata"), {Kind: yamlv3.MappingNode, Content: []*yamlv3.Node{str("name"), str("fragment")}},
		str("spec"), spec,
	}}
	data, err := yamlv3.Marshal(doc)
	if err != nil {
		return v1.AppSpec{}, fault.Internalf(renderOp, "%s: re-encode: %v", file, err)
	}
	obj, err := sdk.DecodeManifest(data)
	if err != nil {
		return v1.AppSpec{}, fault.Invalidf(renderOp, "%s: %s", file, innermost(err))
	}
	app, ok := obj.(*v1.App)
	if !ok {
		return v1.AppSpec{}, fault.Internalf(renderOp, "%s decoded as %T", file, obj)
	}
	return app.Spec, nil
}

// innermost is the message of the innermost fault an error wraps.
func innermost(err error) string {
	var fe *fault.Error
	for errors.As(err, &fe) {
		if fe.Err == nil {
			return fe.Msg
		}
		err = fe.Err
	}
	return err.Error()
}

// origin is where an App section entry comes from: its file and its index in that file.
type origin struct {
	file  string
	index int
}

// appendSections appends each section of src to dst, recording where each entry comes from.
func appendSections(dst *v1.AppSpec, src v1.AppSpec, file string, origins map[string][]origin) {
	dv, sv := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src)
	for i := range dv.NumField() {
		if dv.Field(i).Kind() != reflect.Slice {
			continue
		}
		section, _, _ := strings.Cut(dv.Type().Field(i).Tag.Get("json"), ",")
		for j := range sv.Field(i).Len() {
			origins[section] = append(origins[section], origin{file: file, index: j})
		}
		dv.Field(i).Set(reflect.AppendSlice(dv.Field(i), sv.Field(i)))
	}
	if h := src.Hooks; h != nil && len(h.PreApply)+len(h.PostApply) > 0 {
		if dst.Hooks == nil {
			dst.Hooks = &v1.AppHooks{}
		}
		dst.Hooks.PreApply = appendHooks(dst.Hooks.PreApply, h.PreApply, "hooks.preApply", file, origins)
		dst.Hooks.PostApply = appendHooks(dst.Hooks.PostApply, h.PostApply, "hooks.postApply", file, origins)
	}
}

// appendHooks appends a file's list of one hook point, as appendSections appends a section (ADR-0217 Decision 7).
func appendHooks(dst, src []v1.AppHook, list, file string, origins map[string][]origin) []v1.AppHook {
	for j := range src {
		origins[list] = append(origins[list], origin{file: file, index: j})
	}
	return append(dst, src...)
}

// relocate rewrites each spec.<section>[i] of an App.Validate refusal as <file>: <section>[j].
func relocate(msg string, origins map[string][]origin) string {
	return specEntry.ReplaceAllStringFunc(msg, func(m string) string {
		sub := specEntry.FindStringSubmatch(m)
		i, err := strconv.Atoi(sub[2])
		if o := origins[sub[1]]; err == nil && i < len(o) {
			return o[i].file + ": " + sub[1] + "[" + strconv.Itoa(o[i].index) + "]"
		}
		return m
	})
}
