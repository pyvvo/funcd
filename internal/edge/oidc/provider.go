package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/store"
)

// The keys of the Secrets an OIDCProvider reads and writes (ADR-0335 Decision 1, ADR-0336 Decisions 1 and 2).
const (
	keySessionKey        = "SESSION_KEY"
	keyClientID          = "CLIENT_ID"
	keyClientSecret      = "CLIENT_SECRET"
	keyRegistrationToken = "REGISTRATION_ACCESS_TOKEN"
	keyRegistrationURI   = "REGISTRATION_CLIENT_URI"
	keyTokenSecret       = "REGISTRATION_TOKEN"
)

// sessionKeyBytes is SESSION_KEY's length, AES-256's key (ADR-0335 Decision 5).
const sessionKeyBytes = 32

// The reasons of ADR-0335 Decision 10, in the order the reconciler checks them.
const (
	ReasonSessionSecretMissing = "SessionSecretMissing"
	ReasonSessionKeyInvalid    = "SessionKeyInvalid"
	ReasonClientSecretMissing  = "ClientSecretMissing"
	ReasonAddonBehindProvider  = "AddonBehindProvider"
	ReasonDiscoveryFailed      = "DiscoveryFailed"
	ReasonIssuerMismatch       = "IssuerMismatch"
)

// AddonObject is what the sign-in reads of an add-on object (ADR-0332): its name, its engine's own address
// (status.address, "" while it has none) and its edge address (spec.ingress).
type AddonObject struct {
	Name    v1.ObjectName
	Address string
	Ingress *v1.Ingress
}

// Addons reads a namespace's add-on objects; the platform binds it to the stored AddonObject kind (ADR-0332). Get is
// a fault.NotFound when ns holds no object of that name.
type Addons interface {
	Get(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (AddonObject, error)
	List(ctx context.Context, ns v1.NamespaceName) ([]AddonObject, error)
}

// noAddons is the Addons of a platform without add-on objects.
type noAddons struct{}

func (noAddons) Get(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (AddonObject, error) {
	return AddonObject{}, fault.NotFoundf("oidc.Addons", "add-on object %s/%s not found", ns, name)
}
func (noAddons) List(context.Context, v1.NamespaceName) ([]AddonObject, error) { return nil, nil }

// discovery is what the gate and the reconciler read of a provider's discovery document (OpenID Connect Discovery 1.0
// §3).
type discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	SigningAlgs           []string `json:"id_token_signing_alg_values_supported"`
	AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
}

// checkError is a failed check of ADR-0335 Decision 10: its reason and a message that names no Secret value.
type checkError struct {
	reason  string
	message string
}

func (e *checkError) Error() string { return e.reason + ": " + e.message }

// discover reads issuer's discovery document over c, at the engine's own address when address is set, and checks it
// (ADR-0335 Decisions 4 and 7): its issuer is issuer, and it names the endpoints the sign-in uses, https ones without
// an engine, and an asymmetric signing algorithm.
func discover(ctx context.Context, c *http.Client, issuer, address string) (discovery, *checkError) {
	u, err := rebase(strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", address)
	if err != nil {
		return discovery{}, &checkError{ReasonDiscoveryFailed, err.Error()}
	}
	body, err := get(ctx, c, u)
	if err != nil {
		return discovery{}, &checkError{ReasonDiscoveryFailed, err.Error()}
	}
	var d discovery
	if err := json.Unmarshal(body, &d); err != nil {
		return discovery{}, &checkError{ReasonDiscoveryFailed, fmt.Sprintf("the discovery document at %s is not JSON", u)}
	}
	if d.Issuer != issuer {
		return discovery{}, &checkError{ReasonIssuerMismatch, fmt.Sprintf("the discovery document names the issuer %q, not %q", d.Issuer, issuer)}
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return discovery{}, &checkError{ReasonDiscoveryFailed, "the discovery document names no authorization_endpoint, token_endpoint or jwks_uri"}
	}
	if address == "" {
		for _, e := range []string{d.TokenEndpoint, d.JWKSURI, d.RegistrationEndpoint} {
			if e != "" && !strings.HasPrefix(e, "https://") {
				return discovery{}, &checkError{ReasonDiscoveryFailed, fmt.Sprintf("the discovery document names %s, which is not https", e)}
			}
		}
	}
	if len(d.algs()) == 0 {
		return discovery{}, &checkError{ReasonDiscoveryFailed, "the discovery document lists no RS, PS, ES or EdDSA signing algorithm"}
	}
	return d, nil
}

// algs are the asymmetric algorithms discovery lists (ADR-0335 Decision 4).
func (d discovery) algs() []string {
	var out []string
	for _, a := range asymmetricAlgs() {
		if slices.Contains(d.SigningAlgs, string(a)) {
			out = append(out, string(a))
		}
	}
	return out
}

// authMethod is client_secret_post when discovery lists it, else client_secret_basic (ADR-0335 Decision 4).
func (d discovery) authMethod() string {
	if slices.Contains(d.AuthMethods, methodPost) {
		return methodPost
	}
	return methodBasic
}

const (
	methodPost  = "client_secret_post"
	methodBasic = "client_secret_basic"
)

// source is what a pass or a call reads of one provider from the store: the provider, its Secrets and its add-on
// object. A missing Secret is nil.
type source struct {
	provider *v1.OIDCProvider
	session  *v1.Secret
	client   *v1.Secret
	addon    *AddonObject
}

// read reads the provider ns/name and what it names; a missing provider is a fault.NotFound.
func read(ctx context.Context, st store.Store, addons Addons, ns v1.NamespaceName, name v1.ObjectName) (*source, error) {
	const op = "oidc.read"
	obj, err := st.Get(ctx, v1.KindOIDCProvider.GVK(), ns, name)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get OIDCProvider %s/%s", ns, name)
	}
	p, ok := obj.(*v1.OIDCProvider)
	if !ok {
		return nil, fault.Internalf(op, "object %s/%s is not an OIDCProvider", ns, name)
	}
	s := &source{provider: p}
	if s.session, err = secret(ctx, st, ns, p.Spec.SessionSecret); err != nil {
		return nil, err
	}
	if s.client, err = secret(ctx, st, ns, p.ClientSecretName()); err != nil {
		return nil, err
	}
	if p.Spec.Addon != "" {
		switch a, err := addons.Get(ctx, ns, p.Spec.Addon); {
		case err == nil:
			s.addon = &a
		case fault.KindOf(err) != fault.NotFound:
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "get add-on object %s/%s", ns, p.Spec.Addon)
		}
	}
	return s, nil
}

// secret is the Secret ns/name, nil when it does not exist.
func secret(ctx context.Context, st store.Store, ns v1.NamespaceName, name v1.ObjectName) (*v1.Secret, error) {
	if name == "" {
		return nil, nil
	}
	obj, err := st.Get(ctx, v1.KindSecret.GVK(), ns, name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil, nil
	case err != nil:
		return nil, fault.Wrapf(err, fault.KindOf(err), "oidc.secret", "get Secret %s/%s", ns, name)
	}
	sec, ok := obj.(*v1.Secret)
	if !ok {
		return nil, fault.Internalf("oidc.secret", "object %s/%s is not a Secret", ns, name)
	}
	return sec, nil
}

// check runs ADR-0335 Decision 10's checks before discovery, in its order: the session Secret and key, a
// credentialsSecret's keys, and an add-on object behind the provider. It returns the session key.
func (s *source) check() ([]byte, *checkError) {
	p := s.provider
	if s.session == nil {
		return nil, &checkError{ReasonSessionSecretMissing, fmt.Sprintf("the Secret %q of spec.sessionSecret does not exist", p.Spec.SessionSecret)}
	}
	key, ok := s.session.Spec.Data[keySessionKey]
	if !ok {
		return nil, &checkError{ReasonSessionSecretMissing, fmt.Sprintf("the Secret %q holds no %s", p.Spec.SessionSecret, keySessionKey)}
	}
	if len(key) != sessionKeyBytes {
		return nil, &checkError{ReasonSessionKeyInvalid, fmt.Sprintf("%s of the Secret %q holds %d bytes, not %d", keySessionKey, p.Spec.SessionSecret, len(key), sessionKeyBytes)}
	}
	if p.Spec.Client.Registration == nil && (s.client == nil || len(s.client.Spec.Data[keyClientID]) == 0 || len(s.client.Spec.Data[keyClientSecret]) == 0) {
		return nil, &checkError{ReasonClientSecretMissing, fmt.Sprintf("the Secret %q of spec.client.credentialsSecret does not hold %s and %s", p.Spec.Client.CredentialsSecret, keyClientID, keyClientSecret)}
	}
	if a := s.addon; a != nil {
		if provider, _, ok := a.Ingress.Stance().OIDC(); ok && provider == p.Name {
			return nil, &checkError{ReasonAddonBehindProvider, fmt.Sprintf("the add-on object %q that serves the provider signs its own callers in with it", a.Name)}
		}
	}
	return key, nil
}

// address is the engine's own address the provider is reached at, "" without spec.addon; an add-on object that is
// missing or has no address yet is DiscoveryFailed.
func (s *source) address() (string, *checkError) {
	p := s.provider
	if p.Spec.Addon == "" {
		return "", nil
	}
	if s.addon == nil {
		return "", &checkError{ReasonDiscoveryFailed, fmt.Sprintf("spec.addon %q names no add-on object of namespace %s", p.Spec.Addon, p.Namespace)}
	}
	if s.addon.Address == "" {
		return "", &checkError{ReasonDiscoveryFailed, fmt.Sprintf("the add-on object %q has no engine address yet", p.Spec.Addon)}
	}
	return s.addon.Address, nil
}

// ownClient is the registered client the Secret holds, when the Secret is the provider's own (ADR-0336 Decision 2).
func (s *source) ownClient() (*v1.Secret, bool) {
	if s.client == nil || !ownedstore.Marked(s.client.OwnerReferences, s.provider) {
		return nil, false
	}
	return s.client, true
}

// credentials are the client ID and secret the sign-in uses, empty when the Secret holds none or, under
// registration, is not the provider's own.
func (s *source) credentials() (id, secret string) {
	sec := s.client
	if s.provider.Spec.Client.Registration != nil {
		own, ok := s.ownClient()
		if !ok {
			return "", ""
		}
		sec = own
	}
	if sec == nil {
		return "", ""
	}
	return string(sec.Spec.Data[keyClientID]), string(sec.Spec.Data[keyClientSecret])
}
