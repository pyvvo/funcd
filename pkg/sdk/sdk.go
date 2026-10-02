package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

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

// WithHTTPClient overrides the HTTP client (default http.DefaultClient).
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
	return c, nil
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

// toWireBody marshals obj into the shape huma's request schema requires: a NESTED
// "TypeMeta" object (huma ignores the `,inline` tag; the schema is
// additionalProperties:false + requires TypeMeta), with apiVersion/kind taken
// authoritatively from the object's GVK. (The server re-stamps TypeMeta; responses
// come back flat and decode via decodeObject.)
func toWireBody(obj v1.Object) ([]byte, error) {
	flat, err := json.Marshal(obj)
	if err != nil {
		return nil, fault.Internalf("sdk", "marshal object: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(flat, &m); err != nil {
		return nil, fault.Internalf("sdk", "normalize body: %v", err)
	}
	delete(m, "apiVersion")
	delete(m, "kind")
	// status is server-owned and its embedded Status doesn't round-trip through huma's
	// nested-schema (like TypeMeta); never send it on a write.
	delete(m, "status")
	gvk := obj.GroupVersionKind()
	tm, err := json.Marshal(v1.TypeMeta{APIVersion: gvk.APIVersion(), Kind: gvk.Kind})
	if err != nil {
		return nil, fault.Internalf("sdk", "marshal typemeta: %v", err)
	}
	m["TypeMeta"] = tm
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

// DecodeManifest parses a flat JSON resource manifest (apiVersion/kind/metadata/spec)
// into the concrete v1.Object. An unknown/empty kind is a fault.Invalid, never a panic.
func DecodeManifest(data []byte) (v1.Object, error) {
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
	if err := yaml.Unmarshal(data, obj); err != nil {
		return nil, fault.Invalidf("sdk.DecodeManifest", "decode %s: %v", tm.Kind, err)
	}
	return obj, nil
}

// problemToFault maps a non-2xx response to a typed fault.Error. It keys on the JSON
// `status` field (handler faults arrive as application/json, huma's own 422/401 as
// application/problem+json) — never on the Content-Type.
func problemToFault(httpStatus int, body []byte) error {
	var p fault.Problem
	_ = json.Unmarshal(body, &p) // best-effort; falls back to httpStatus
	status := p.Status
	if status == 0 {
		status = httpStatus
	}
	msg := p.Detail
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
