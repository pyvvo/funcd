package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// apiPrefix is the control-plane REST prefix (ADR-0005/0018).
const apiPrefix = "/apis/funcd.io/v1alpha1"

// Client is a typed Go client over the funcd control-plane REST API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

// Option configures a Client (functional-options facade, ADR-0002).
type Option func(*Client)

// WithHTTPClient overrides the HTTP client (default http.DefaultClient). A client without a
// CheckRedirect policy gets the SDK's, which refuses a redirect that would change the method.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// WithToken sets the bearer token sent on every request (empty = unauthenticated).
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// New builds a Client. baseURL (e.g. "http://localhost:8080") is required.
func New(baseURL string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fault.Invalidf("sdk.New", "baseURL is required")
	}
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: http.DefaultClient,
	}
	for _, o := range opts {
		o(c)
	}
	if c.httpClient.CheckRedirect == nil {
		hc := *c.httpClient
		hc.CheckRedirect = refuseMethodChange
		c.httpClient = &hc
	}
	return c, nil
}

// refuseMethodChange stops a redirect that changes the method: on a 301/302/303 Go resends a
// PUT/POST/DELETE as a body-less GET, so the write is lost while the GET's 2xx reads as success.
// Method-preserving redirects keep Go's default policy (at most 10 hops).
func refuseMethodChange(req *http.Request, via []*http.Request) error {
	if orig := via[0].Method; req.Method != orig {
		return fmt.Errorf("refusing a %d redirect to %s that would resend %s as %s; point the server URL at the final address",
			req.Response.StatusCode, req.URL, orig, req.Method)
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// Apply create-or-replaces obj: PUT the named path; on a 404 (the object does not
// yet exist) POST the collection path to create it. Returns the stored object.
func (c *Client) Apply(ctx context.Context, obj v1.Object) (v1.Object, error) {
	kind := obj.GroupVersionKind().Kind
	if ReadOnlyKind(kind) {
		return nil, errReadOnly("sdk.Apply", kind)
	}
	ns := obj.GetNamespace()
	name := obj.GetName()
	body, err := toWireBody(obj)
	if err != nil {
		return nil, err
	}
	if name == "" {
		// Server-side name generation (ObjectMeta.GenerateName): no name to key a PUT on, so POST to the
		// collection and let the server assign Name = GenerateName + <id>. A missing name AND no
		// GenerateName is a client error.
		if obj.GetObjectMeta().GenerateName == "" {
			return nil, fault.Invalidf("sdk.Apply", "object has no name and no generateName")
		}
		return c.create(ctx, kind, ns, body)
	}
	itemURL, err := c.itemURL(kind, ns, name)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPut, itemURL, body)
	if err != nil {
		if fault.KindOf(err) != fault.NotFound {
			return nil, err
		}
		return c.create(ctx, kind, ns, body)
	}
	return decodeObject(kind, resp)
}

// Create creates obj by POSTing the collection path, never replacing an existing object: a taken
// name is a fault.Conflict. An empty name with GenerateName set lets the server assign one.
func (c *Client) Create(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if k := obj.GroupVersionKind().Kind; ReadOnlyKind(k) {
		return nil, errReadOnly("sdk.Create", k)
	}
	body, err := toWireBody(obj)
	if err != nil {
		return nil, err
	}
	return c.create(ctx, obj.GroupVersionKind().Kind, obj.GetNamespace(), body)
}

func (c *Client) create(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, body []byte) (v1.Object, error) {
	colURL, err := c.collectionURL(kind, ns)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, colURL, body)
	if err != nil {
		return nil, err
	}
	return decodeObject(kind, resp)
}

// Get fetches one object.
func (c *Client) Get(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	itemURL, err := c.itemURL(kind, ns, name)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, itemURL, nil)
	if err != nil {
		return nil, err
	}
	return decodeObject(kind, resp)
}

// List fetches every object of a kind in a namespace.
func (c *Client) List(ctx context.Context, kind v1.Kind, ns v1.NamespaceName) ([]v1.Object, error) {
	colURL, err := c.collectionURL(kind, ns)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, colURL, nil)
	if err != nil {
		return nil, err
	}
	// The list body is a bare JSON array (huma Output.Body []v1.X); allocate each
	// element via NewObject (you cannot json.Unmarshal into a []v1.Object slice).
	var raw []json.RawMessage
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, fault.Internalf("sdk.List", "decode %s list: %v", kind, err)
	}
	out := make([]v1.Object, 0, len(raw))
	for _, elem := range raw {
		obj, derr := decodeObject(kind, elem)
		if derr != nil {
			return nil, derr
		}
		out = append(out, obj)
	}
	return out, nil
}

// Delete removes one object.
func (c *Client) Delete(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName, opts ...DeleteOption) error {
	if ReadOnlyKind(kind) {
		return errReadOnly("sdk.Delete", kind)
	}
	var o deleteOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.force && kind != v1.KindResourceGroup {
		return fault.Invalidf("sdk.Delete", "force applies only to a ResourceGroup, not %s", kind)
	}
	itemURL, err := c.itemURL(kind, ns, name)
	if err != nil {
		return err
	}
	if o.force {
		itemURL += "?force=true"
	}
	var hdr http.Header
	if o.version != "" {
		hdr = http.Header{"If-Match": {`"` + o.version + `"`}}
	}
	_, err = c.send(ctx, http.MethodDelete, itemURL, nil, hdr)
	return err
}

// HandoverKVStore makes workflow the owner of the kept KVStore store, so that Workflow binds it again (ADR-0178).
// The caller needs KVStore update and delete and Workflow get in ns.
func (c *Client) HandoverKVStore(ctx context.Context, ns v1.NamespaceName, store, workflow v1.ObjectName) error {
	itemURL, err := c.itemURL(v1.KindKVStore, ns, store)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]v1.ObjectName{"workflow": workflow})
	if err != nil {
		return fault.Internalf("sdk.HandoverKVStore", "marshal: %v", err)
	}
	_, err = c.do(ctx, http.MethodPost, itemURL+"/handover", body)
	return err
}

// RetryApp starts again, without waiting, the failed hook of the App's latest revision (ADR-0214 Decision 7). The
// caller needs App update in ns; fault.Conflict when the App has no failed hook, a hook call of it runs or it is
// paused.
func (c *Client) RetryApp(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	itemURL, err := c.itemURL(v1.KindApp, ns, name)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, itemURL+"/retry", nil)
	return err
}

// DeleteOption tunes a Delete.
type DeleteOption func(*deleteOptions)

type deleteOptions struct {
	force   bool
	version string
}

// Force deletes a ResourceGroup's members first, each under its own protections, then the group (ADR-0170).
// Delete refuses it with fault.Invalid for any other kind, before a request.
func Force() DeleteOption { return func(o *deleteOptions) { o.force = true } }

// IfVersion makes Delete conditional on rv: fault.Conflict when the object's resourceVersion differs (ADR-0210).
func IfVersion(rv string) DeleteOption { return func(o *deleteOptions) { o.version = rv } }

// collectionURL builds the collection path for a kind (and namespace, if namespaced).
func (c *Client) collectionURL(kind v1.Kind, ns v1.NamespaceName) (string, error) {
	d, ok := descriptorFor(kind)
	if !ok {
		return "", fault.Invalidf("sdk", "unknown kind %q", kind)
	}
	if d.namespaced {
		if ns == "" {
			return "", fault.Invalidf("sdk", "kind %q is namespaced; a namespace is required", kind)
		}
		nsURL, err := c.namespaceURL(ns)
		if err != nil {
			return "", err
		}
		return nsURL + "/" + d.plural, nil
	}
	return c.baseURL + apiPrefix + "/" + d.plural, nil
}

// itemURL builds the named path for a kind. The name must be a DNS label: any other text could add a path
// segment, a query or a fragment to the URL, and so reach another object (issue #698).
func (c *Client) itemURL(kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) (string, error) {
	col, err := c.collectionURL(kind, ns)
	if err != nil {
		return "", err
	}
	if err := name.Validate(); err != nil {
		return "", err
	}
	return col + "/" + string(name), nil
}

// namespaceURL builds the path of namespace ns, which must be a DNS label for the same reason.
func (c *Client) namespaceURL(ns v1.NamespaceName) (string, error) {
	if err := ns.Validate(); err != nil {
		return "", err
	}
	return c.baseURL + apiPrefix + "/namespaces/" + string(ns), nil
}

// do executes an HTTP request, returning the 2xx body or a typed fault.Error.
func (c *Client) do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	return c.send(ctx, method, url, body, nil)
}

// send is do with extra request headers.
func (c *Client) send(ctx context.Context, method, url string, body []byte, hdr http.Header) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fault.Internalf("sdk", "build request: %v", err)
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fault.Unavailablef("sdk", "%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fault.Internalf("sdk", "read response: %v", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, problemToFault(resp.StatusCode, respBody)
	}
	return respBody, nil
}

// toWireBody marshals obj with apiVersion/kind taken authoritatively from the object's GVK.
// Status is server-owned, so a write never sends it. (The server re-stamps TypeMeta.)
func toWireBody(obj v1.Object) ([]byte, error) {
	flat, err := json.Marshal(obj)
	if err != nil {
		return nil, fault.Internalf("sdk", "marshal object: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(flat, &m); err != nil {
		return nil, fault.Internalf("sdk", "normalize body: %v", err)
	}
	delete(m, "status")
	gvk := obj.GroupVersionKind()
	if m["apiVersion"], err = json.Marshal(gvk.APIVersion()); err != nil {
		return nil, fault.Internalf("sdk", "marshal apiVersion: %v", err)
	}
	if m["kind"], err = json.Marshal(gvk.Kind); err != nil {
		return nil, fault.Internalf("sdk", "marshal kind: %v", err)
	}
	return json.Marshal(m)
}

// decodeObject unmarshals a response body (flat, stdlib-marshaled by huma) into the
// concrete kind via v1.NewObject.
func decodeObject(kind v1.Kind, body []byte) (v1.Object, error) {
	obj, ok := v1.NewObject(kind)
	if !ok {
		return nil, fault.Invalidf("sdk", "unknown kind %q", kind)
	}
	if err := json.Unmarshal(body, obj); err != nil {
		return nil, fault.Internalf("sdk", "decode %s: %v", kind, err)
	}
	return obj, nil
}

// DecodeManifest parses a single-document resource manifest (apiVersion/kind/metadata/spec)
// into the concrete v1.Object. An unknown/empty kind is a fault.Invalid, never a panic; so is a
// manifest holding more than one document (DecodeManifests decodes each).
func DecodeManifest(data []byte) (v1.Object, error) {
	objs, err := DecodeManifests(data)
	if err != nil {
		return nil, err
	}
	if len(objs) != 1 {
		return nil, fault.Invalidf("sdk.DecodeManifest", "manifest holds %d documents, want 1", len(objs))
	}
	return objs[0], nil
}

// DecodeManifests parses a manifest that may hold several YAML documents separated by "---" into
// one v1.Object per document, in order. Empty and comment-only documents are skipped; a manifest
// with no document is a fault.Invalid.
func DecodeManifests(data []byte) ([]v1.Object, error) {
	docs, err := DecodeManifestDocuments(data)
	if err != nil {
		return nil, err
	}
	objs := make([]v1.Object, len(docs))
	for i, doc := range docs {
		objs[i] = doc.Object
	}
	return objs, nil
}

// ManifestDocument is one decoded manifest document and its 1-based position in the manifest,
// counted as the decode errors count it (skipped empty and comment-only documents included).
type ManifestDocument struct {
	Number int
	Object v1.Object
}

// DecodeManifestDocuments is DecodeManifests that also returns each object's document number, so a
// caller can name the document a later error belongs to (issue #316).
func DecodeManifestDocuments(data []byte) ([]ManifestDocument, error) {
	const op = "sdk.DecodeManifests"
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	var docs []ManifestDocument
	for n := 1; ; n++ {
		var doc yamlv3.Node
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fault.Invalidf(op, "parse manifest document %d: %v", n, err)
		}
		if len(doc.Content) == 0 || doc.Content[0].ShortTag() == "!!null" {
			continue
		}
		quoteStrings(&doc)
		obj, err := decodeDocument(&doc)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "manifest document %d", n)
		}
		docs = append(docs, ManifestDocument{Number: n, Object: obj})
	}
	if len(docs) == 0 {
		return nil, fault.Invalidf(op, "manifest holds no document")
	}
	return docs, nil
}

// decodeDocument decodes one manifest document into its concrete v1.Object.
func decodeDocument(doc *yamlv3.Node) (v1.Object, error) {
	data, err := yamlv3.Marshal(doc)
	if err != nil {
		return nil, fault.Invalidf("sdk.DecodeManifest", "re-encode manifest: %v", err)
	}
	// sigs.k8s.io/yaml accepts YAML *and* JSON (JSON is valid YAML), so `apply` takes either —
	// the kubectl-style manifest experience, reusing the api/types json tags.
	var tm v1.TypeMeta
	if err := yaml.Unmarshal(data, &tm); err != nil {
		return nil, fault.Invalidf("sdk.DecodeManifest", "parse manifest: %v", err)
	}
	if tm.Kind == "" {
		return nil, fault.Invalidf("sdk.DecodeManifest", "manifest is missing 'kind'")
	}
	obj, ok := v1.NewObject(tm.Kind)
	if !ok {
		return nil, fault.Invalidf("sdk.DecodeManifest", "unknown kind %q", tm.Kind)
	}
	quoteTextScalars(doc, reflect.TypeOf(obj))
	if data, err = yamlv3.Marshal(doc); err != nil {
		return nil, fault.Invalidf("sdk.DecodeManifest", "re-encode manifest: %v", err)
	}
	// Strict: a key the typed object lacks would be dropped by toWireBody before the server's
	// additionalProperties:false edge could reject it (ADR-0108), so the apply would report success.
	if err := yaml.UnmarshalStrict(data, obj); err != nil {
		return nil, fault.Invalidf("sdk.DecodeManifest", "decode %s: %v", tm.Kind, err)
	}
	return obj, nil
}

// quoteStrings double-quotes every plain scalar, key or value, that YAML 1.2 reads as a string.
// sigs.k8s.io/yaml decodes YAML 1.1, which turns a bare on/off/yes/no/y/n into a boolean: a key becomes
// "true"/"false" that the typed decode drops (issue #63), a string value is rewritten to "true"/"false"
// and a json.RawMessage value becomes a JSON boolean (issue #299).
func quoteStrings(n *yamlv3.Node) {
	if n.Kind == yamlv3.ScalarNode && n.Style == 0 && n.ShortTag() == "!!str" {
		n.Style = yamlv3.DoubleQuotedStyle
	}
	for _, c := range n.Content {
		quoteStrings(c)
	}
}

// quoteTextScalars double-quotes every plain scalar that YAML reads as a number or a boolean when it lands
// in a string: a mapping key, or a value whose target type t (walked by json tags) is a string. sigs.k8s.io/yaml
// would parse it and format the value back, so 1.10, 0755 and 0x1F would reach a string field as "1.1", "493"
// and "31" (issue #416). A value bound for a number, bool, json.RawMessage or any field keeps its type.
func quoteTextScalars(n *yamlv3.Node, t reflect.Type) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch n.Kind {
	case yamlv3.DocumentNode:
		for _, c := range n.Content {
			quoteTextScalars(c, t)
		}
	case yamlv3.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			quoteTextScalar(key)
			var vt reflect.Type
			switch {
			case t == nil:
			case t.Kind() == reflect.Map:
				vt = t.Elem()
			case t.Kind() == reflect.Struct:
				vt = jsonFieldType(t, key.Value)
			}
			quoteTextScalars(n.Content[i+1], vt)
		}
	case yamlv3.SequenceNode:
		var et reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			et = t.Elem()
		}
		for _, c := range n.Content {
			quoteTextScalars(c, et)
		}
	case yamlv3.ScalarNode:
		if t != nil && t.Kind() == reflect.String {
			quoteTextScalar(n)
		}
	}
}

func quoteTextScalar(n *yamlv3.Node) {
	if n.Kind != yamlv3.ScalarNode || n.Style != 0 {
		return
	}
	switch n.ShortTag() {
	case "!!int", "!!float", "!!bool":
		n.Tag = "!!str"
		n.Style = yamlv3.DoubleQuotedStyle
	}
}

// jsonFieldType returns the type of the field of struct t that encoding/json decodes key into: an exact
// name match first, then a case-insensitive one. It returns nil when no field matches.
func jsonFieldType(t reflect.Type, key string) reflect.Type {
	var fold reflect.Type
	for _, f := range jsonFields(t) {
		if f.Name == key {
			return f.Type
		}
		if fold == nil && strings.EqualFold(f.Name, key) {
			fold = f.Type
		}
	}
	return fold
}

// jsonFields lists the fields of struct t under their json names, with the fields of an untagged or
// `json:",inline"` embedded struct promoted unless an outer field has the same name.
func jsonFields(t reflect.Type) []reflect.StructField {
	var own, promoted []reflect.StructField
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if et := f.Type; f.Anonymous && name == "" {
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				promoted = append(promoted, jsonFields(et)...)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name != "" {
			f.Name = name
		}
		own = append(own, f)
	}
	for _, p := range promoted {
		if !slices.ContainsFunc(own, func(o reflect.StructField) bool { return o.Name == p.Name }) {
			own = append(own, p)
		}
	}
	return own
}

// problemToFault maps a non-2xx response to a typed fault.Error. It keys on the JSON
// `status` field of the problem+json body — never on the Content-Type. huma's errors[] (field,
// reason, value) is appended to the message: on a 422 it is the only place that names the bad field.
func problemToFault(httpStatus int, body []byte) error {
	var p struct {
		fault.Problem
		Errors []huma.ErrorDetail `json:"errors"`
	}
	_ = json.Unmarshal(body, &p) // best-effort; falls back to httpStatus
	status := p.Status
	if status == 0 {
		status = httpStatus
	}
	msg := p.Detail
	if len(p.Errors) > 0 {
		details := make([]string, 0, len(p.Errors))
		for i := range p.Errors {
			details = append(details, p.Errors[i].Error())
		}
		joined := strings.Join(details, "; ")
		if msg == "" {
			msg = joined
		} else {
			msg += ": " + joined
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch status {
	case http.StatusNotFound:
		return fault.NotFoundf("sdk", "%s", msg)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fault.Invalidf("sdk", "%s", msg)
	case http.StatusConflict:
		return fault.Conflictf("sdk", "%s", msg)
	case http.StatusUnauthorized:
		return fault.Unauthorizedf("sdk", "%s", msg)
	case http.StatusForbidden:
		return fault.Forbiddenf("sdk", "%s", msg)
	case http.StatusServiceUnavailable:
		return fault.Unavailablef("sdk", "%s", msg)
	case http.StatusTooManyRequests:
		return fault.ResourceExhaustedf("sdk", "%s", msg)
	case http.StatusRequestEntityTooLarge:
		return fault.PayloadTooLargef("sdk", "%s", msg)
	default:
		return fault.Internalf("sdk", "%s", msg)
	}
}
