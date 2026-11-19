package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// clientMetadata is the client funcd registers (RFC 7591 §2, ADR-0336 Decision 2); a PUT names its client_id (RFC 7592
// §2.2).
type clientMetadata struct {
	ClientID                string   `json:"client_id,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ClientName              string   `json:"client_name"`
}

func metadata(p *v1.OIDCProvider, callbacks []string, method string) clientMetadata {
	return clientMetadata{RedirectURIs: callbacks, GrantTypes: []string{"authorization_code"}, TokenEndpointAuthMethod: method,
		ClientName: fmt.Sprintf("funcd %s %s", p.Namespace, p.Name)}
}

// clientInfo is a registration's answer, to a POST, a GET or a PUT (RFC 7591 §3.2.1, RFC 7592 §3).
type clientInfo struct {
	ClientID          string   `json:"client_id"`
	ClientSecret      string   `json:"client_secret"`
	RegistrationToken string   `json:"registration_access_token"`
	RegistrationURI   string   `json:"registration_client_uri"`
	RedirectURIs      []string `json:"redirect_uris"`
}

// stored is the client the Secret holds.
type stored struct {
	id, secret, token, uri string
}

func storedOf(sec *v1.Secret) *stored {
	if sec == nil {
		return nil
	}
	d := sec.Spec.Data
	s := &stored{id: string(d[keyClientID]), secret: string(d[keyClientSecret]), token: string(d[keyRegistrationToken]),
		uri: string(d[keyRegistrationURI])}
	if s.id == "" || s.uri == "" {
		return nil
	}
	return s
}

// gone is a client the provider no longer knows (ADR-0336 Decision 3).
func gone(status int) bool { return status == http.StatusNotFound || status == http.StatusUnauthorized }

// masked is a read's secret that triggers nothing: left out, or masked with asterisks (ADR-0336 Decision 3).
func masked(secret string) bool { return strings.Trim(secret, "*") == "" }

// sameSet reports whether a and b hold the same URIs, in any order.
func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// registrationError is a failed registration call: its message names the HTTP status, never the answer's body
// (ADR-0336 Decision 6).
type registrationError struct {
	status int
	cause  string
}

func (e *registrationError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("%s answered %d %s", e.cause, e.status, http.StatusText(e.status))
	}
	return e.cause
}

// register calls the provider: method u with the registration token and the client's metadata, and returns the answer's
// status and the client it describes. A transport failure is a registrationError with no status; an answer that is
// not 2xx is one with its status, and gone tells a client the provider lost.
func register(ctx context.Context, c *http.Client, method, u, token string, body *clientMetadata) (clientInfo, int, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return clientInfo{}, 0, fault.Wrapf(err, fault.Internal, "oidc.register", "encode the client")
		}
		payload = bytes.NewReader(b)
	}
	what := fmt.Sprintf("%s %s", method, u)
	req, err := http.NewRequestWithContext(ctx, method, u, payload)
	if err != nil {
		return clientInfo{}, 0, &registrationError{cause: what + ": not a valid request"}
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return clientInfo{}, 0, &registrationError{cause: what + ": " + err.Error()}
	}
	defer httpx.CloseBody(resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return clientInfo{}, resp.StatusCode, &registrationError{status: resp.StatusCode, cause: what}
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return clientInfo{}, resp.StatusCode, &registrationError{cause: what + ": the answer could not be read"}
	}
	var info clientInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return clientInfo{}, resp.StatusCode, &registrationError{cause: what + ": the answer is not a client"}
	}
	return info, resp.StatusCode, nil
}
