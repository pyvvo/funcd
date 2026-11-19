package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
)

const condReady = v1.ConditionType("Ready")

// The client's reasons (ADR-0336 Decision 7): ClientReady on Ready while a usable client exists, the others on Ready
// without one and on ClientUpkeep with one. ClientUpkeep is True with reasonApplied once a pass applied the callbacks
// and rotate.
const (
	ReasonClientReady        = "ClientReady"
	ReasonSecretNotOwned     = "SecretNotOwned"
	ReasonUnused             = "Unused"
	ReasonRegistrationFailed = "RegistrationFailed"
	ReasonSecretIssueFailed  = "SecretIssueFailed"
	ReasonRotationRefused    = "RotationRefused"
	reasonApplied            = "Applied"
)

// ReconcilerDeps configures the OIDCProvider reconciler (ADR-0336 Contracts). Client reaches a provider outside funcd
// (nil: ADR-0335 Decision 7's back channel); an add-on engine is reached at its own address. BaseDomain names the
// namespace hosts (ADR-0240), Addons the add-on objects (nil: none).
type ReconcilerDeps struct {
	Store             store.Store
	CallbackURL       func(host string) string
	Client            *http.Client
	Held              hold.NamespaceGate
	SupervisionPeriod time.Duration
	BaseDomain        string
	Addons            Addons
	Logger            *slog.Logger
}

// Reconciler is the OIDCProvider kind's one reconciler (ADR-0336 Decision 7): ADR-0335 Decision 10's checks, then the
// client's registration and upkeep.
type Reconciler struct {
	store    store.Store
	callback func(host string) string
	back     backChannel
	held     hold.NamespaceGate
	period   time.Duration
	base     string
	addons   Addons
	log      *slog.Logger
	clock    clock.Clock

	mu      sync.Mutex
	refused map[providerKey]time.Time // the last rotation a provider refused
}

// NewReconciler builds the reconciler.
func NewReconciler(d ReconcilerDeps) *Reconciler {
	r := &Reconciler{store: d.Store, callback: d.CallbackURL, back: newBackChannel(nil), held: d.Held, period: d.SupervisionPeriod,
		base: d.BaseDomain, addons: d.Addons, log: d.Logger, clock: clock.System(), refused: map[providerKey]time.Time{}}
	if d.Client != nil {
		r.back.global = d.Client
	}
	if r.held == nil {
		r.held = hold.NeverNamespace
	}
	if r.period <= 0 {
		r.period = controller.SupervisionPeriod
	}
	if r.addons == nil {
		r.addons = noAddons{}
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	r.log = r.log.With("component", "edge.oidc.reconciler")
	if r.callback == nil {
		r.callback = func(host string) string { return "http://" + host + callbackPath }
	}
	return r
}

// BackChannelClient is the client of a provider's calls over rt, with ADR-0335 Decision 7's timeout and size cap; nil
// rt is Decision 7's transport.
func BackChannelClient(rt http.RoundTripper) *http.Client { return newBackChannel(rt).global }

// Reconcile runs one pass over the provider, each runtime.supervisionPeriod and on every change of it, its Secrets or
// a watched object; it makes no call while the namespace is held (ADR-0290, ADR-0336 Decision 3).
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "oidc.Reconcile"
	if r.held.HeldIn(req.Namespace) {
		return controller.Result{RequeueAfter: r.period}, nil
	}
	s, err := read(ctx, r.store, r.addons, req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, nil // the owner collector deletes the client Secret; the provider purges the client
	}
	if err != nil {
		return controller.Result{}, err
	}
	p := s.provider
	before, _ := json.Marshal(p.Status)
	if err := r.pass(ctx, s); err != nil {
		return controller.Result{}, err
	}
	p.Status.ObservedGeneration = p.Generation
	if after, _ := json.Marshal(p.Status); string(after) != string(before) {
		if _, err := r.store.Update(ctx, p); err != nil {
			if fault.KindOf(err) == fault.Conflict {
				return controller.Result{Requeue: true}, nil
			}
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "update OIDCProvider %s/%s status", p.Namespace, p.Name)
		}
	}
	return controller.Result{RequeueAfter: r.period}, nil
}

// pass runs ADR-0335 Decision 10's checks in order, a failing one ending the pass with Ready=False, then the client.
func (r *Reconciler) pass(ctx context.Context, s *source) error {
	p := s.provider
	if _, cerr := s.check(); cerr != nil {
		notReady(p, cerr.reason, cerr.message)
		return nil
	}
	address, cerr := s.address()
	if cerr != nil {
		notReady(p, cerr.reason, cerr.message)
		return nil
	}
	c := r.back.client(address)
	d, cerr := discover(ctx, c, p.Spec.Issuer, address)
	if cerr != nil {
		notReady(p, cerr.reason, cerr.message)
		return nil
	}
	if p.Spec.Client.Registration == nil {
		id, _ := s.credentials()
		p.Status.ClientID = id
		ready(p)
		p.Status.Conditions = slices.DeleteFunc(p.Status.Conditions, func(c v1.Condition) bool { return c.Type == v1.ConditionClientUpkeep })
		return nil
	}
	return r.upkeep(ctx, s, d, address, c)
}

// upkeepPass is one pass's inputs: the callbacks and rotate it read at its start (ADR-0336 Decision 3).
type upkeepPass struct {
	s         *source
	d         discovery
	address   string
	client    *http.Client
	callbacks []string
	rotate    int64
}

// upkeep registers or keeps the client (ADR-0336 Decisions 2, 3 and 7).
func (r *Reconciler) upkeep(ctx context.Context, s *source, d discovery, address string, c *http.Client) error {
	p := s.provider
	callbacks, err := r.callbacks(ctx, p)
	if err != nil {
		return err
	}
	u := upkeepPass{s: s, d: d, address: address, client: c, callbacks: callbacks, rotate: p.Spec.Client.Registration.Rotate}
	own, ok := s.ownClient()
	if s.client != nil && !ok {
		notReady(p, ReasonSecretNotOwned, fmt.Sprintf("the Secret %q exists and funcd did not write it for this provider; it is left alone", p.ClientSecretName()))
		return nil
	}
	cur := storedOf(own)
	if len(callbacks) == 0 {
		msg := "no host signs callers in with this provider"
		if cur != nil {
			p.Status.ClientID = cur.id
			ready(p)
			upkept(p, ReasonUnused, msg+"; the client is kept")
		} else {
			notReady(p, ReasonUnused, msg+"; nothing is registered")
			upkept(p, ReasonUnused, msg)
		}
		return nil
	}
	if cur == nil {
		return r.registerNew(ctx, u, "")
	}
	uri, err := rebase(cur.uri, address)
	if err != nil {
		return r.registerNew(ctx, u, "the stored registration_client_uri is not a URL")
	}
	info, status, err := register(ctx, c, http.MethodGet, uri, cur.token, nil)
	switch {
	case gone(status):
		return r.registerNew(ctx, u, fmt.Sprintf("the provider no longer knows the client (%d)", status))
	case err != nil:
		ready(p)
		upkept(p, ReasonRegistrationFailed, "the client could not be read: "+err.Error())
		return nil
	}
	token := cmpOr(info.RegistrationToken, cur.token)
	stale := !masked(info.ClientSecret) && info.ClientSecret != cur.secret
	drift := !slices.Equal(callbacks, p.Status.Callbacks) || !sameSet(callbacks, info.RedirectURIs)
	pending := u.rotate > p.Status.ObservedRotate
	// A refused rotation goes again once each supervision period, no faster.
	rotation := pending && !r.refusedRecently(p)
	if !stale && !drift && !rotation {
		if token != cur.token {
			next := *cur
			next.token = token
			if err := r.write(ctx, p, next); err != nil {
				return r.issueFailed(p, true, err)
			}
		}
		p.Status.ClientID = cur.id
		ready(p)
		if !pending {
			upkept(p, "", "")
		}
		return nil
	}
	return r.update(ctx, u, *cur, uri, token)
}

// update sends RFC 7592's PUT and stores its answer, then records the callbacks and rotate the pass read; a rotation
// whose answer keeps the stored secret is RotationRefused, and a gone client is registered again.
func (r *Reconciler) update(ctx context.Context, u upkeepPass, cur stored, uri, token string) error {
	p := u.s.provider
	body := metadata(p, u.callbacks, u.d.authMethod())
	body.ClientID = cur.id
	info, status, err := register(ctx, u.client, http.MethodPut, uri, token, &body)
	switch {
	case gone(status):
		return r.registerNew(ctx, u, fmt.Sprintf("the provider no longer knows the client (%d)", status))
	case err != nil:
		ready(p)
		upkept(p, ReasonRegistrationFailed, "the client could not be updated: "+err.Error())
		return nil
	}
	answered := info.ClientSecret
	if masked(answered) {
		answered = ""
	}
	next := stored{id: cmpOr(info.ClientID, cur.id), secret: cmpOr(answered, cur.secret),
		token: cmpOr(info.RegistrationToken, token), uri: cmpOr(info.RegistrationURI, cur.uri)}
	refused := u.rotate > p.Status.ObservedRotate && next.secret == cur.secret
	if next != cur {
		if err := r.write(ctx, p, next); err != nil {
			return r.issueFailed(p, true, err)
		}
	}
	p.Status.Callbacks, p.Status.ClientID = u.callbacks, next.id
	ready(p)
	if refused {
		r.mu.Lock()
		r.refused[providerKey{p.Namespace, p.Name}] = r.clock.Now()
		r.mu.Unlock()
		upkept(p, ReasonRotationRefused, fmt.Sprintf("the provider kept the client's secret on the update; rotate %d is sent again each %s", u.rotate, r.period))
		return nil
	}
	p.Status.ObservedRotate = u.rotate
	upkept(p, "", "")
	return nil
}

// registerNew registers the client (RFC 7591 POST), stores the answer and records what the pass read.
func (r *Reconciler) registerNew(ctx context.Context, u upkeepPass, why string) error {
	p := u.s.provider
	if why != "" {
		r.log.InfoContext(ctx, "registering the client again", "namespace", p.Namespace, "provider", p.Name, "why", why)
	}
	fail := func(msg string) error {
		notReady(p, ReasonRegistrationFailed, msg)
		upkept(p, ReasonRegistrationFailed, msg)
		return nil
	}
	if u.d.RegistrationEndpoint == "" {
		return fail("the discovery document names no registration_endpoint")
	}
	token := ""
	if name := p.Spec.Client.Registration.TokenSecret; name != "" {
		sec, err := secret(ctx, r.store, p.Namespace, name)
		if err != nil {
			return err
		}
		if sec == nil || len(sec.Spec.Data[keyTokenSecret]) == 0 {
			return fail(fmt.Sprintf("the Secret %q of spec.client.registration.tokenSecret holds no %s", name, keyTokenSecret))
		}
		token = string(sec.Spec.Data[keyTokenSecret])
	}
	endpoint, err := rebase(u.d.RegistrationEndpoint, u.address)
	if err != nil {
		return fail(err.Error())
	}
	body := metadata(p, u.callbacks, u.d.authMethod())
	info, _, err := register(ctx, u.client, http.MethodPost, endpoint, token, &body)
	if err != nil {
		return fail("the client could not be registered: " + err.Error())
	}
	next := stored{id: info.ClientID, secret: info.ClientSecret, token: info.RegistrationToken, uri: info.RegistrationURI}
	if next.id == "" || next.secret == "" || next.uri == "" {
		return fail("the registration's answer holds no client_id, client_secret or registration_client_uri")
	}
	if err := r.write(ctx, p, next); err != nil {
		return r.issueFailed(p, false, err)
	}
	p.Status.Callbacks, p.Status.ObservedRotate, p.Status.ClientID = u.callbacks, u.rotate, next.id
	ready(p)
	upkept(p, "", "")
	return nil
}

// issueFailed reports a failed Secret write: on ClientUpkeep while a usable client exists, else on Ready. A Secret
// another wrote since the pass began is SecretNotOwned.
func (r *Reconciler) issueFailed(p *v1.OIDCProvider, usable bool, err error) error {
	reason, msg := ReasonSecretIssueFailed, "the client could not be written to its Secret: "+err.Error()
	if errors.Is(err, ownedstore.ErrNotOwned) {
		reason, msg = ReasonSecretNotOwned, fmt.Sprintf("the Secret %q exists and funcd did not write it for this provider; it is left alone", p.ClientSecretName())
	}
	if usable && reason == ReasonSecretIssueFailed {
		ready(p)
		upkept(p, reason, msg)
		return nil
	}
	notReady(p, reason, msg)
	upkept(p, reason, msg)
	return nil
}

// write writes the client into the Secret, owned by the provider as an Identity owns its credential (ADR-0135,
// ADR-0336 Decision 2): a Secret of that name funcd did not write is ownedstore.ErrNotOwned.
func (r *Reconciler) write(ctx context.Context, p *v1.OIDCProvider, c stored) error {
	obj, _ := v1.NewObject(v1.KindSecret)
	sec, _ := obj.(*v1.Secret)
	sec.Namespace, sec.Name, sec.ResourceGroup = p.Namespace, p.ClientSecretName(), p.ResourceGroup
	sec.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{keyClientID: []byte(c.id),
		keyClientSecret: []byte(c.secret), keyRegistrationToken: []byte(c.token), keyRegistrationURI: []byte(c.uri)}}
	_, err := ownedstore.Ensure(ctx, r.store, p, sec, v1.DeletionDelete)
	return err
}

func (r *Reconciler) refusedRecently(p *v1.OIDCProvider) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.refused[providerKey{p.Namespace, p.Name}]
	return ok && r.clock.Now().Sub(at) < r.period
}

// callbacks are the sorted callbacks of every host whose effective stance names p (ADR-0336 Decision 2): a rule's,
// else its Route's, else the Namespace's edgeDefaults.auth; a Function served by name has its ingress.auth, else the
// Namespace's, on the namespace host; an add-on object has its own ingress.auth only (ADR-0332 Decision 5).
func (r *Reconciler) callbacks(ctx context.Context, p *v1.OIDCProvider) ([]string, error) {
	const op = "oidc.callbacks"
	ns := p.Namespace
	nsSpec, err := namespaceSpec(ctx, r.store, ns)
	if err != nil {
		return nil, err
	}
	var nsAuth *v1.RouteAuth
	if d := nsSpec.EdgeDefaults; d != nil {
		nsAuth = d.Auth.RouteAuth()
	}
	nsHost := router.NamespaceHost(ns, r.base)
	hosts := map[string]bool{}
	names := func(a *v1.RouteAuth) bool { provider, _, ok := a.OIDC(); return ok && provider == p.Name }
	routes, err := r.store.List(ctx, v1.KindRoute.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the Routes of %s", ns)
	}
	for _, o := range routes.Items {
		rt, ok := o.(*v1.Route)
		if !ok || rt.Spec.Enabled != nil && !*rt.Spec.Enabled {
			continue
		}
		for i := range rt.Spec.Rules {
			if names(cmpAuth(rt.Spec.Rules[i].Auth, rt.Spec.Auth, nsAuth)) {
				hosts[cmpOr(rt.Spec.Host, nsHost)] = true
			}
		}
	}
	fns, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the Functions of %s", ns)
	}
	for _, o := range fns.Items {
		fn, ok := o.(*v1.Function)
		if !ok || !fn.Spec.Ingress.EnabledIn(nsSpec.DefaultExposure) {
			continue
		}
		if ing := fn.Spec.Ingress; ing != nil && (ing.Host != "" || ing.Path != "") {
			continue // its own Route, listed above
		}
		if names(cmpAuth(fn.Spec.Ingress.Stance(), nsAuth)) {
			hosts[nsHost] = true
		}
	}
	addons, err := r.addons.List(ctx, ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the add-on objects of %s", ns)
	}
	for _, a := range addons {
		if a.Ingress != nil && names(a.Ingress.Stance()) {
			hosts[cmpOr(a.Ingress.Host, nsHost)] = true
		}
	}
	out := []string{}
	for h := range hosts {
		if h != "" {
			out = append(out, r.callback(h))
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// namespaceSpec is ns's spec; an absent Namespace is the zero spec.
func namespaceSpec(ctx context.Context, st store.Store, ns v1.NamespaceName) (v1.NamespaceSpec, error) {
	obj, err := st.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return v1.NamespaceSpec{}, nil
	case err != nil:
		return v1.NamespaceSpec{}, fault.Wrapf(err, fault.KindOf(err), "oidc.namespaceSpec", "get Namespace %s", ns)
	}
	n, _ := obj.(*v1.Namespace)
	if n == nil {
		return v1.NamespaceSpec{}, nil
	}
	return n.Spec, nil
}

// cmpAuth is the first stance set.
func cmpAuth(stances ...*v1.RouteAuth) *v1.RouteAuth {
	for _, a := range stances {
		if a != nil && a.Mode != "" {
			return a
		}
	}
	return nil
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func ready(p *v1.OIDCProvider) {
	p.Status.Phase = v1.PhaseReady
	p.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: ReasonClientReady, ObservedGeneration: p.Generation})
}

func notReady(p *v1.OIDCProvider, reason, message string) {
	p.Status.Phase = v1.PhasePending
	p.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: message, ObservedGeneration: p.Generation})
}

// upkept sets ClientUpkeep: True when reason is "", else False with reason.
func upkept(p *v1.OIDCProvider, reason, message string) {
	c := v1.Condition{Type: v1.ConditionClientUpkeep, Status: v1.ConditionTrue, Reason: reasonApplied, ObservedGeneration: p.Generation}
	if reason != "" {
		c.Status, c.Reason, c.Message = v1.ConditionFalse, reason, message
	}
	p.Status.Conditions.Set(c)
}
