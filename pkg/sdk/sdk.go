package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
		colURL, cerr := c.collectionURL(kind, ns)
		if cerr != nil {
			return nil, cerr
		}
		resp, derr := c.do(ctx, http.MethodPost, colURL, body)
		if derr != nil {
			return nil, derr
		}
		return decodeObject(kind, resp)
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
		colURL, cerr := c.collectionURL(kind, ns)
		if cerr != nil {
			return nil, cerr
		}
		resp, err = c.do(ctx, http.MethodPost, colURL, body)
		if err != nil {
			return nil, err
		}
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
func (c *Client) Delete(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) error {
	itemURL, err := c.itemURL(kind, ns, name)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, itemURL, nil)
	return err
}

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
		return c.baseURL + apiPrefix + "/namespaces/" + string(ns) + "/" + d.plural, nil
	}
	return c.baseURL + apiPrefix + "/" + d.plural, nil
}

// itemURL builds the named path for a kind.
func (c *Client) itemURL(kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) (string, error) {
	col, err := c.collectionURL(kind, ns)
	if err != nil {
		return "", err
	}
	return col + "/" + string(name), nil
}

// do executes an HTTP request, returning the 2xx body or a typed fault.Error.
func (c *Client) do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fault.Internalf("sdk", "build request: %v", err)
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
	const op = "sdk.DecodeManifests"
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	var objs []v1.Object
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
		quoteKeys(&doc)
		raw, err := yamlv3.Marshal(&doc)
		if err != nil {
			return nil, fault.Invalidf(op, "re-encode manifest document %d: %v", n, err)
		}
		obj, err := decodeDocument(raw)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "manifest document %d", n)
		}
		objs = append(objs, obj)
	}
	if len(objs) == 0 {
		return nil, fault.Invalidf(op, "manifest holds no document")
	}
	return objs, nil
}

// decodeDocument decodes one manifest document into its concrete v1.Object.
func decodeDocument(data []byte) (v1.Object, error) {
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
	// Strict: a key the typed object lacks would be dropped by toWireBody before the server's
	// additionalProperties:false edge could reject it (ADR-0108), so the apply would report success.
	if err := yaml.UnmarshalStrict(data, obj); err != nil {
		return nil, fault.Invalidf("sdk.DecodeManifest", "decode %s: %v", tm.Kind, err)
	}
	return obj, nil
}

// quoteKeys double-quotes every plain mapping key that YAML 1.2 reads as a string. sigs.k8s.io/yaml
// decodes YAML 1.1, which turns a bare on/off/yes/no/y/n key into a boolean (the JSON key "true"/"false")
// that the typed decode then drops (issue #63); values keep the YAML 1.1 decode.
func quoteKeys(n *yamlv3.Node) {
	if n.Kind == yamlv3.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			if k := n.Content[i]; k.Kind == yamlv3.ScalarNode && k.Style == 0 && k.ShortTag() == "!!str" {
				k.Style = yamlv3.DoubleQuotedStyle
			}
		}
	}
	for _, c := range n.Content {
		quoteKeys(c)
	}
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
	default:
		return fault.Internalf("sdk", "%s", msg)
	}
}
