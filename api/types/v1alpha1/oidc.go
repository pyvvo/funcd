package v1alpha1

import (
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// OIDCProvider is an OpenID Connect provider the edge signs users in with (ADR-0335, FEAT-0018 F261), and whose
// sign-in client funcd registers and keeps under client.registration (ADR-0336, F262).
type OIDCProvider struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       OIDCProviderSpec   `json:"spec"`
	Status     OIDCProviderStatus `json:"status,omitempty"`
}

// OIDCProviderSpec names the issuer, the client and the session key (ADR-0335 Decision 1).
type OIDCProviderSpec struct {
	Issuer        string             `json:"issuer" doc:"The provider's issuer URL, as its discovery document names it and as browsers reach it. Without addon, funcd reads the discovery document and every endpoint it names over https only, at addresses reachable from the internet; with addon, at the add-on engine's own address."`
	Audiences     []string           `json:"audiences,omitempty" doc:"The aud values a Bearer token may carry to sign a call in. Empty, the default, means that a Bearer token signs nobody in."`
	Client        OIDCProviderClient `json:"client" doc:"The sign-in client: set exactly one of registration, a client funcd registers at the provider, and credentialsSecret, a client made by hand."`
	SessionSecret ObjectName         `json:"sessionSecret" doc:"A Secret of the namespace whose SESSION_KEY holds 32 random bytes, the key that seals the sign-in cookies. A new key ends every session of the provider."`
	Addon         ObjectName         `json:"addon,omitempty" doc:"The add-on object of the namespace that serves the provider: funcd reaches the provider at the engine's own address, while browsers use issuer."`
}

// OIDCProviderClient is the client funcd signs users in as: exactly one of Registration and CredentialsSecret.
type OIDCProviderClient struct {
	Registration      *OIDCRegistration `json:"registration,omitempty" doc:"funcd registers the client at the provider's registration endpoint, keeps its redirect URIs on the callbacks of the hosts that sign in with the provider, and writes its credentials to a Secret it owns."`
	CredentialsSecret ObjectName        `json:"credentialsSecret,omitempty" doc:"A Secret of the namespace, written by hand, whose CLIENT_ID and CLIENT_SECRET hold a client made at the provider."`
}

// OIDCRegistration is a client funcd registers and keeps (ADR-0336 Decision 1).
type OIDCRegistration struct {
	TokenSecret          ObjectName `json:"tokenSecret,omitempty" doc:"A Secret whose REGISTRATION_TOKEN funcd sends as a Bearer token with every registration, for a provider that requires one."`
	CredentialSecretName ObjectName `json:"credentialSecretName,omitempty" doc:"The Secret funcd writes the client to, as CLIENT_ID, CLIENT_SECRET, REGISTRATION_ACCESS_TOKEN and REGISTRATION_CLIENT_URI, and deletes with the provider. Empty means the provider's name. funcd leaves alone a Secret of that name it did not write, and the provider then stays Pending with reason SecretNotOwned."`
	Rotate               int64      `json:"rotate,omitempty" minimum:"0" doc:"Raise it above status.observedRotate to rotate the client's secret: funcd asks the provider for a new one and stores it."`
}

// OIDCProviderStatus is the shared Status with the client funcd keeps (ADR-0336 Contracts).
type OIDCProviderStatus struct {
	Status         `json:",inline"`
	ClientID       string   `json:"clientId,omitempty" doc:"The client ID the stored client carries."`
	Callbacks      []string `json:"callbacks,omitempty" doc:"The callbacks funcd last applied to the registered client's redirect URIs, sorted."`
	ObservedRotate int64    `json:"observedRotate,omitempty" doc:"The spec.client.registration.rotate value funcd last applied: a rotation is done once it equals rotate."`
}

// ConditionClientUpkeep reports whether a pass applied the callbacks and rotate to the registered client, beside
// Ready (ADR-0336 Decision 7).
const ConditionClientUpkeep = ConditionType("ClientUpkeep")

// GroupVersionKind returns the constant GVK for OIDCProvider.
func (p *OIDCProvider) GroupVersionKind() GroupVersionKind { return KindOIDCProvider.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (p *OIDCProvider) GetStatus() *Status { return &p.Status.Status }

// ClientSecretName is the Secret that holds the client: credentialSecretName, else the provider's name, under
// registration; credentialsSecret otherwise.
func (p *OIDCProvider) ClientSecretName() ObjectName {
	r := p.Spec.Client.Registration
	switch {
	case r == nil:
		return p.Spec.Client.CredentialsSecret
	case r.CredentialSecretName != "":
		return r.CredentialSecretName
	default:
		return p.Name
	}
}

// Validate checks the envelope and the spec's shape. That the Secrets, the add-on object and the provider exist and
// answer is the reconciler's (ADR-0335 Decision 10).
func (p *OIDCProvider) Validate() error {
	const op = "OIDCProvider.Validate"
	if err := validateMeta(p.TypeMeta, &p.ObjectMeta, KindOIDCProvider); err != nil {
		return err
	}
	if err := validateIssuer(p.Spec.Issuer); err != nil {
		return fault.Invalidf(op, "spec.issuer %q: %v", p.Spec.Issuer, err)
	}
	for i, a := range p.Spec.Audiences {
		if strings.TrimSpace(a) == "" {
			return fault.Invalidf(op, "spec.audiences[%d] is empty", i)
		}
	}
	c := p.Spec.Client
	if (c.Registration == nil) == (c.CredentialsSecret == "") {
		return fault.Invalidf(op, "spec.client must set exactly one of registration and credentialsSecret")
	}
	if p.Spec.SessionSecret == "" {
		return fault.Invalidf(op, "spec.sessionSecret is required")
	}
	fields := []string{"spec.sessionSecret", "spec.addon", "spec.client.credentialsSecret"}
	names := []ObjectName{p.Spec.SessionSecret, p.Spec.Addon, c.CredentialsSecret}
	if r := c.Registration; r != nil {
		if r.Rotate < 0 {
			return fault.Invalidf(op, "spec.client.registration.rotate %d must not be negative", r.Rotate)
		}
		fields = append(fields, "spec.client.registration.tokenSecret", "spec.client.registration.credentialSecretName")
		names = append(names, r.TokenSecret, r.CredentialSecretName)
	}
	for i, n := range names {
		if n != "" && !dnsLabel.MatchString(string(n)) {
			return fault.Invalidf(op, "%s %q is not a valid DNS-1123 label", fields[i], n)
		}
	}
	return nil
}

// validateIssuer accepts an absolute http or https URL with a host and no user, query or fragment (OpenID Connect
// Discovery 1.0 §2). Whether an http issuer is reachable is the reconciler's (ADR-0335 Decision 7).
func validateIssuer(issuer string) error {
	u, err := url.Parse(issuer)
	switch {
	case issuer == "":
		return fault.Invalidf("validateIssuer", "it is required")
	case err != nil:
		return fault.Invalidf("validateIssuer", "it is not a URL")
	case u.Scheme != "https" && u.Scheme != "http":
		return fault.Invalidf("validateIssuer", "its scheme must be https or http")
	case u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return fault.Invalidf("validateIssuer", "it must name a host, and no user, query or fragment")
	}
	return nil
}

// Unauthenticated is what an oidc stance gives a call that is not signed in (ADR-0335 Decision 2): "" is redirect.
type Unauthenticated string

const (
	// UnauthenticatedRedirect sends a GET or HEAD navigation to the provider with 302, and any other call gets 401.
	UnauthenticatedRedirect Unauthenticated = "redirect"
	// Unauthenticated401 gives every call that is not signed in 401.
	Unauthenticated401 Unauthenticated = "401"
	// UnauthenticatedAllow passes every call, with no principal when it is not signed in.
	UnauthenticatedAllow Unauthenticated = "allow"
)

// Schema renders the huma enum for Unauthenticated (empty accepted, meaning redirect).
func (Unauthenticated) Schema(huma.Registry) *huma.Schema {
	return enumSchema("", string(UnauthenticatedRedirect), string(Unauthenticated401), string(UnauthenticatedAllow))
}

// Normalized maps the empty value to redirect.
func (u Unauthenticated) Normalized() Unauthenticated {
	if u == "" {
		return UnauthenticatedRedirect
	}
	return u
}

// AuthPath is funcd's own path on every host (ADR-0335 Decision 3): no rule, ingress.path or add-on basePath may lie at
// or under it.
const AuthPath = "/.auth"

// UnderAuthPath reports whether p is AuthPath or lies under it.
func UnderAuthPath(p string) bool {
	return p == AuthPath || strings.HasPrefix(p, AuthPath+"/")
}

// validateStance checks an edge stance (ADR-0113, ADR-0335 Decision 2): its mode, the provider oidc requires and
// unauthenticated, both of which another mode refuses.
func validateStance(op, field string, mode AuthMode, provider ObjectName, un Unauthenticated) error {
	switch mode {
	case "", AuthAuthenticated, AuthOpen:
		if provider != "" || un != "" {
			return fault.Invalidf(op, "%s.provider and %s.unauthenticated are only for mode oidc", field, field)
		}
		return nil
	case AuthOIDC:
	default:
		return fault.Invalidf(op, "%s.mode %q must be authenticated, open or oidc", field, mode)
	}
	if provider == "" {
		return fault.Invalidf(op, "%s.provider is required with mode oidc", field)
	}
	if !dnsLabel.MatchString(string(provider)) {
		return fault.Invalidf(op, "%s.provider %q is not a valid DNS-1123 label", field, provider)
	}
	switch un {
	case "", UnauthenticatedRedirect, Unauthenticated401, UnauthenticatedAllow:
		return nil
	default:
		return fault.Invalidf(op, "%s.unauthenticated %q must be redirect, 401 or allow", field, un)
	}
}

// defaultNamespace has no namespace host (ADR-0240 Decision 2), so an oidc stance there needs a host of its own.
const defaultNamespace NamespaceName = "default"

// hostlessOIDC refuses an oidc stance with no host in the default namespace (ADR-0335 Decision 3).
func hostlessOIDC(op, field string, ns NamespaceName, host string) error {
	if ns == defaultNamespace && host == "" {
		return fault.Invalidf(op, "%s: mode oidc in namespace default needs a host, since default has no namespace host", field)
	}
	return nil
}
