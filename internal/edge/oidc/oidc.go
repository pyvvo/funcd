// Package oidc is the edge's sign-in with an OpenID Connect provider (ADR-0335, FEAT-0018 F261) and the one reconciler
// of the OIDCProvider kind, which also registers and keeps the edge's client at the provider (ADR-0336, F262).
//
// The Gate answers a call on an oidc stance: a session cookie or, with spec.audiences, a Bearer token of the provider
// signs it in with the four principal headers set; a call that is not signed in is redirected to the provider, refused
// 401 or allowed. It keeps no session on the server: the state and session cookies are sealed with the provider's
// SESSION_KEY, and it caches only the provider's discovery and signing keys.
package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/secrets/aesgcm"
	"github.com/pyvvo/funcd/internal/store"
)

// The principal headers (ADR-0335 Decision 8): only the gate sets them, for a signed-in call.
const (
	HeaderPrincipal     = "X-Funcd-Principal"
	HeaderPrincipalID   = "X-Funcd-Principal-Id"
	HeaderPrincipalName = "X-Funcd-Principal-Name"
	HeaderIDToken       = "X-Funcd-Id-Token"
)

// Strip removes the four principal headers from h.
func Strip(h http.Header) {
	for _, k := range []string{HeaderPrincipal, HeaderPrincipalID, HeaderPrincipalName, HeaderIDToken} {
		h.Del(k)
	}
}

// funcd's paths under /.auth/ (ADR-0335 Decision 3).
const (
	callbackPath = v1.AuthPath + "/callback"
	logoutPath   = v1.AuthPath + "/logout"
)

// The cookies (ADR-0335 Decision 5): <kind>.<namespace>.<provider>, under https with the __Host- prefix. The state
// lives stateTTL, the session until the ID token's exp; a session cookie over maxCookieBytes signs nobody in.
const (
	stateCookie    = "funcd_auth"
	sessionCookie  = "funcd_session"
	hostPrefix     = "__Host-"
	stateTTL       = 10 * time.Minute
	maxCookieBytes = 4096
	randomBytes    = 32
)

// scopes are what the sign-in asks the provider for (ADR-0335 Decision 4).
func scopes() []string { return []string{oidc.ScopeOpenID, "profile", "email"} }

// Deps configures the Gate (ADR-0335 Contracts); Addons reaches an add-on object's engine address, nil meaning none.
type Deps struct {
	Store       store.Store              // OIDCProviders and their Secrets
	CallbackURL func(host string) string // Decision 9
	HTTPS       bool                     // Decision 9: Secure __Host- cookies
	BackChannel http.RoundTripper        // nil: Decision 7's
	Addons      Addons                   // the add-on objects a provider's spec.addon names (ADR-0332)
	Logger      *slog.Logger
}

// Gate is the edge's sign-in: it keeps each provider's discovery, keys and last key fetch.
type Gate struct {
	store    store.Store
	callback func(host string) string
	https    bool
	addons   Addons
	back     backChannel
	log      *slog.Logger
	clock    clock.Clock

	mu     sync.Mutex
	views  map[providerKey]*view
	logged map[providerKey]string
}

type providerKey struct {
	ns   v1.NamespaceName
	name v1.ObjectName
}

// view is the gate's reading of one provider, kept while stamp holds.
type view struct {
	providerKey
	stamp      stamp
	issuer     string
	audiences  []string
	disc       discovery
	address    string
	client     *http.Client
	keys       *keySet
	sessionKey []byte
	clientID   string
	secret     string
}

// stamp is what the cached discovery depends on: the provider, its Secrets and its engine's address.
type stamp struct {
	uid        v1.UID
	generation int64
	session    string
	client     string
	address    string
}

// New builds the Gate.
func New(d Deps) *Gate {
	g := &Gate{store: d.Store, callback: d.CallbackURL, https: d.HTTPS, addons: d.Addons, back: newBackChannel(d.BackChannel),
		log: d.Logger, clock: clock.System(), views: map[providerKey]*view{}, logged: map[providerKey]string{}}
	if g.addons == nil {
		g.addons = noAddons{}
	}
	if g.log == nil {
		g.log = slog.Default()
	}
	g.log = g.log.With("component", "edge.oidc")
	if g.callback == nil {
		g.callback = func(host string) string { return "http://" + host + callbackPath }
	}
	return g
}

const op = "edge.oidc"

// Serve returns true when the call goes on, signed in or allowed, and false when it answered the call itself.
func (g *Gate) Serve(w http.ResponseWriter, r *http.Request, ns v1.NamespaceName, a router.OIDCAuth) bool {
	ctx := r.Context()
	v, err := g.load(ctx, ns, a.Provider)
	if err != nil {
		g.unavailable(ctx, w, providerKey{ns, a.Provider}, err)
		return false
	}
	if claims, idToken, ok := g.session(r, v); ok {
		if !safeMethod(r.Method) && !sameOrigin(r, g.scheme()) {
			fault.WriteProblem(w, fault.Forbiddenf(op, "a %s with a session must come from the host's own origin", r.Method))
			return false
		}
		signIn(r, a.Provider, claims, idToken)
		return true
	}
	if token, ok := bearer(r); ok && len(v.audiences) > 0 {
		claims, err := g.verifyBearer(ctx, v, token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			fault.WriteProblem(w, fault.Unauthorizedf(op, "the Bearer token does not sign the call in: %v", err))
			return false
		}
		signIn(r, a.Provider, claims, "")
		return true
	}
	switch a.Unauthenticated.Normalized() {
	case v1.UnauthenticatedAllow:
		dropCookies(r)
		return true
	case v1.UnauthenticatedRedirect:
		if navigation(r) {
			g.redirect(w, r, v)
			return false
		}
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	fault.WriteProblem(w, fault.Unauthorizedf(op, "sign in first"))
	return false
}

// Auth answers a path under /.auth/, on any host: the callback, the logout, else 404.
func (g *Gate) Auth(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case callbackPath:
		g.signInCallback(w, r)
	case logoutPath:
		g.logout(w, r)
	default:
		fault.WriteProblem(w, fault.NotFoundf(op, "no route for %q", r.URL.Path))
	}
}

// errProviderMissing is a stance whose provider is not in its namespace.
var errProviderMissing = errors.New("the OIDCProvider does not exist")

// load is the provider's view: the Secrets and the client read now, the discovery and keys from the cache while the
// stamp holds (ADR-0335 Decisions 7 and 10). An error means the provider has no usable client or cannot be reached.
func (g *Gate) load(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*view, error) {
	s, err := read(ctx, g.store, g.addons, ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, errProviderMissing
	}
	if err != nil {
		return nil, err
	}
	key, cerr := s.check()
	if cerr != nil {
		return nil, cerr
	}
	id, secret := s.credentials()
	if id == "" || secret == "" || !clientUsable(s.provider) {
		return nil, errors.New("the provider has no usable client")
	}
	address, cerr := s.address()
	if cerr != nil {
		return nil, cerr
	}
	st := stamp{uid: s.provider.UID, generation: s.provider.Generation, session: s.session.ResourceVersion, address: address}
	if s.client != nil {
		st.client = s.client.ResourceVersion
	}
	k := providerKey{ns, name}
	g.mu.Lock()
	cached := g.views[k]
	g.mu.Unlock()
	v := &view{providerKey: k, stamp: st, issuer: s.provider.Spec.Issuer, audiences: s.provider.Spec.Audiences,
		address: address, client: g.back.client(address), sessionKey: key, clientID: id, secret: secret}
	if cached != nil && cached.stamp == st {
		v.disc, v.keys = cached.disc, cached.keys
	} else {
		d, cerr := discover(ctx, v.client, v.issuer, address)
		if cerr != nil {
			return nil, cerr
		}
		v.disc = d
		if cached != nil && cached.disc.JWKSURI == d.JWKSURI && cached.address == address {
			v.keys = cached.keys
		} else {
			jwks, err := rebase(d.JWKSURI, address)
			if err != nil {
				return nil, err
			}
			v.keys = newKeySet(v.client, jwks, g.clock)
		}
	}
	if _, err := v.keys.current(ctx); err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.views[k] = v
	delete(g.logged, k)
	g.mu.Unlock()
	return v, nil
}

// clientReasons are the Ready reasons of a provider with no usable client (ADR-0336 Decision 7).
func clientReasons() []string {
	return []string{ReasonSecretNotOwned, ReasonUnused, ReasonRegistrationFailed, ReasonSecretIssueFailed}
}

// clientUsable reports whether a registration's last pass left a usable client: Ready is not False for want of one.
func clientUsable(p *v1.OIDCProvider) bool {
	if p.Spec.Client.Registration == nil {
		return true
	}
	c, ok := p.Status.Conditions.Get(condReady)
	return !ok || c.Status != v1.ConditionFalse || !slices.Contains(clientReasons(), c.Reason)
}

// unavailable answers 503, logging each provider's cause once until it is usable again: the detail names no cause.
func (g *Gate) unavailable(ctx context.Context, w http.ResponseWriter, k providerKey, cause error) {
	g.mu.Lock()
	logged := g.logged[k] == cause.Error()
	g.logged[k] = cause.Error()
	g.mu.Unlock()
	if !logged {
		g.log.WarnContext(ctx, "edge sign-in unavailable", "namespace", k.ns, "provider", k.name, "error", cause)
	}
	fault.WriteProblem(w, fault.Unavailablef(op, "sign-in is unavailable"))
}

func (g *Gate) scheme() string {
	if g.https {
		return "https"
	}
	return "http"
}

// cookieName is a cookie of kind for the provider, under https with the __Host- prefix.
func (g *Gate) cookieName(kind string, k providerKey) string {
	name := kind + "." + string(k.ns) + "." + string(k.name)
	if g.https {
		return hostPrefix + name
	}
	return name
}

// cookie is a cookie of the gate's: HttpOnly, SameSite=Lax and Path=/, Secure under https.
func (g *Gate) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: g.https,
		SameSite: http.SameSiteLaxMode}
}

// additionalData binds a sealed cookie to its name, its provider and the provider's client (ADR-0335 Decision 5).
func additionalData(name string, v *view) []byte {
	return []byte(strings.Join([]string{name, string(v.ns), string(v.name), v.clientID}, "\x00"))
}

func (g *Gate) seal(v *view, name string, plain []byte) (string, error) {
	sealed, err := aesgcm.Seal(v.sessionKey, plain, additionalData(name, v))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open is the cookie's JSON, false when it does not open under the provider's key and additional data.
func (g *Gate) open(v *view, name, value string) ([]byte, bool) {
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, false
	}
	plain, err := aesgcm.Open(v.sessionKey, sealed, additionalData(name, v))
	return plain, err == nil
}

// sessionData is the session cookie's sealed JSON.
type sessionData struct {
	IDToken string `json:"idToken"`
}

// stateData is the state cookie's sealed JSON: the sign-in's state, nonce and PKCE verifier, and the first path.
type stateData struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Path     string `json:"path"`
	Expires  int64  `json:"exp"`
}

// session is the call's session of the provider: the ID token's claims and the token, when the cookie opens and the
// token's iss, aud and exp hold (ADR-0335 Decision 5).
func (g *Gate) session(r *http.Request, v *view) (json.RawMessage, string, bool) {
	name := g.cookieName(sessionCookie, v.providerKey)
	c, err := r.Cookie(name)
	if err != nil {
		return nil, "", false
	}
	var s sessionData
	if plain, ok := g.open(v, name, c.Value); !ok || json.Unmarshal(plain, &s) != nil {
		return nil, "", false
	}
	payload, err := tokenPayload(s.IDToken)
	if err != nil {
		return nil, "", false
	}
	var claims struct {
		Issuer   string   `json:"iss"`
		Audience audience `json:"aud"`
		Expiry   float64  `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Issuer != v.issuer || !slices.Contains(claims.Audience, v.clientID) ||
		float64(g.clock.Now().Unix()) >= claims.Expiry {
		return nil, "", false
	}
	return payload, s.IDToken, true
}

// audience is a JWT's aud: one string or a list of them.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// tokenPayload is a JWT's payload, unverified: a session's token was verified at the callback, and the seal keeps it
// whole since.
func tokenPayload(raw string) (json.RawMessage, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fault.Invalidf(op, "the token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !json.Valid(payload) {
		return nil, fault.Invalidf(op, "the token's payload is not JSON")
	}
	return payload, nil
}

// verifyBearer checks a Bearer token with go-oidc, the client ID check off, then its aud against spec.audiences
// (ADR-0335 Decision 6).
func (g *Gate) verifyBearer(ctx context.Context, v *view, token string) (json.RawMessage, error) {
	idt, err := g.verifier(v, "").Verify(ctx, token)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(idt.Audience, func(a string) bool { return slices.Contains(v.audiences, a) }) {
		return nil, fault.Unauthorizedf(op, "the token's audience is none of spec.audiences")
	}
	var claims json.RawMessage
	if err := idt.Claims(&claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// verifier checks a token's signature with the provider's keys, an asymmetric algorithm discovery lists, its iss and
// exp and, unless clientID is "", that aud holds clientID (ADR-0335 Decision 4).
func (g *Gate) verifier(v *view, clientID string) *oidc.IDTokenVerifier {
	return oidc.NewVerifier(v.issuer, v.keys, &oidc.Config{ClientID: clientID, SkipClientIDCheck: clientID == "",
		SupportedSigningAlgs: v.disc.algs(), Now: g.clock.Now})
}

// principal is the X-Funcd-Principal JSON.
type principal struct {
	Provider string          `json:"provider"`
	Claims   json.RawMessage `json:"claims"`
}

// signIn sets the principal headers from the token's claims and removes funcd's cookies from the call (ADR-0335
// Decision 8).
func signIn(r *http.Request, provider v1.ObjectName, claims json.RawMessage, idToken string) {
	b, err := json.Marshal(principal{Provider: string(provider), Claims: claims})
	if err == nil {
		r.Header.Set(HeaderPrincipal, base64.StdEncoding.EncodeToString(b))
	}
	var c struct {
		Subject           string `json:"sub"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	_ = json.Unmarshal(claims, &c)
	if headerValue(c.Subject) {
		r.Header.Set(HeaderPrincipalID, c.Subject)
	}
	for _, n := range []string{c.Name, c.PreferredUsername, c.Email} {
		if headerValue(n) {
			r.Header.Set(HeaderPrincipalName, n)
			break
		}
	}
	if idToken != "" {
		r.Header.Set(HeaderIDToken, idToken)
	}
	dropCookies(r)
}

// headerValue reports whether s is a non-empty header value with no control character.
func headerValue(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool { return c < ' ' || c == 0x7f })
}

// funcdCookie reports whether name is one of the gate's cookies, of either form.
func funcdCookie(name string) bool {
	name = strings.TrimPrefix(name, hostPrefix)
	return strings.HasPrefix(name, stateCookie+".") || strings.HasPrefix(name, sessionCookie+".")
}

// dropCookies removes funcd's cookies from the call's Cookie headers and keeps the others as sent.
func dropCookies(r *http.Request) {
	var kept []string
	for _, line := range r.Header.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if part = strings.TrimSpace(part); part != "" && !funcdCookie(name) {
				kept = append(kept, part)
			}
		}
	}
	r.Header.Del("Cookie")
	if len(kept) > 0 {
		r.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// safeMethod is a method a session call from another origin may use (ADR-0335 Decision 5).
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin reports whether the call comes from the host's own origin: Sec-Fetch-Site: same-origin, or, without that
// header, no Origin or the host's (ADR-0335 Decision 5).
func sameOrigin(r *http.Request, scheme string) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}

// navigation is a GET or HEAD a browser navigates with: Sec-Fetch-Mode: navigate, or else text/html in Accept
// (ADR-0335 Decision 2).
func navigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// bearer is the call's Authorization: Bearer token.
func bearer(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}

// host is the call's host as the callback names it: lower case, with no port or trailing dot.
func host(r *http.Request) string {
	h := r.Host
	if hp, _, err := net.SplitHostPort(h); err == nil {
		h = hp
	}
	return strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
}

// oauth2Config is the code flow's client for the provider, with the host's callback (ADR-0335 Decisions 4 and 9).
func (g *Gate) oauth2Config(v *view, callback string) (*oauth2.Config, error) {
	token, err := rebase(v.disc.TokenEndpoint, v.address)
	if err != nil {
		return nil, err
	}
	style := oauth2.AuthStyleInHeader
	if v.disc.authMethod() == methodPost {
		style = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{ClientID: v.clientID, ClientSecret: v.secret, RedirectURL: callback, Scopes: scopes(),
		Endpoint: oauth2.Endpoint{AuthURL: v.disc.AuthorizationEndpoint, TokenURL: token, AuthStyle: style}}, nil
}

// redirect sends a navigation to the provider with a new state cookie: the state, the nonce, the PKCE verifier and
// the first path (ADR-0335 Decision 4).
func (g *Gate) redirect(w http.ResponseWriter, r *http.Request, v *view) {
	st := stateData{State: random(), Nonce: random(), Verifier: oauth2.GenerateVerifier(), Path: r.URL.RequestURI(),
		Expires: g.clock.Now().Add(stateTTL).Unix()}
	cfg, err := g.oauth2Config(v, g.callback(host(r)))
	if err != nil {
		g.unavailable(r.Context(), w, v.providerKey, err)
		return
	}
	plain, _ := json.Marshal(st)
	name := g.cookieName(stateCookie, v.providerKey)
	value, err := g.seal(v, name, plain)
	if err != nil {
		g.unavailable(r.Context(), w, v.providerKey, err)
		return
	}
	http.SetCookie(w, g.cookie(name, value, int(stateTTL/time.Second)))
	http.Redirect(w, r, cfg.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier),
		oauth2.SetAuthURLParam("nonce", st.Nonce)), http.StatusFound)
}

func random() string {
	b := make([]byte, randomBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// signInCallback answers /.auth/callback for the provider its state cookie names (ADR-0335 Decision 4): it checks the
// state, exchanges the code, checks the ID token and sets the session, or answers 401 naming the cause.
func (g *Gate) signInCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	v, name, st, err := g.stateOf(r, q.Get("state"))
	switch {
	case err != nil:
		g.unavailable(ctx, w, v.providerKey, err)
		return
	case v == nil:
		fault.WriteProblem(w, fault.Unauthorizedf(op, "the sign-in's state cookie is missing or holds another state: sign in again"))
		return
	}
	// The state is used once: every answer below clears it, after the session it may set.
	clearState := g.cookie(name, "", -1)
	refuse := func(detail string) {
		http.SetCookie(w, clearState)
		fault.WriteProblem(w, fault.Unauthorizedf(op, "%s", detail))
	}
	if g.clock.Now().Unix() > st.Expires {
		refuse("the sign-in's state expired: sign in again")
		return
	}
	if e := q.Get("error"); e != "" {
		refuse(fmt.Sprintf("the provider refused the sign-in: %s", queryToken(e)))
		return
	}
	code := q.Get("code")
	if code == "" {
		refuse("the provider sent no code")
		return
	}
	v, raw, err := g.exchange(ctx, v, code, g.callback(host(r)), st.Verifier)
	if err != nil {
		g.log.InfoContext(ctx, "edge sign-in: the code exchange failed", "namespace", v.ns, "provider", v.name, "error", err)
		refuse("the provider did not exchange the code: sign in again")
		return
	}
	expiry, err := g.verifyIDToken(ctx, v, raw, st.Nonce)
	if err != nil {
		refuse(fmt.Sprintf("the ID token is not valid: %v", err))
		return
	}
	plain, _ := json.Marshal(sessionData{IDToken: raw})
	sessionName := g.cookieName(sessionCookie, v.providerKey)
	value, err := g.seal(v, sessionName, plain)
	if err != nil {
		g.unavailable(ctx, w, v.providerKey, err)
		return
	}
	if n := len(sessionName) + len(value); n > maxCookieBytes {
		refuse(fmt.Sprintf("the session cookie would hold %d bytes, over %d: the provider's ID token is too large", n, maxCookieBytes))
		return
	}
	http.SetCookie(w, g.cookie(sessionName, value, int(expiry.Sub(g.clock.Now())/time.Second)))
	http.SetCookie(w, clearState)
	http.Redirect(w, r, firstPath(st.Path), http.StatusFound)
}

// stateOf is the provider of the call's state cookie that opens and holds state, with the cookie's name and data;
// v is nil when none does. An error is a provider that cannot be used.
func (g *Gate) stateOf(r *http.Request, state string) (*view, string, stateData, error) {
	var unusable error
	var unusableKey providerKey
	for _, c := range r.Cookies() {
		k, ok := g.parseCookie(c.Name, stateCookie)
		if !ok || state == "" {
			continue
		}
		v, err := g.load(r.Context(), k.ns, k.name)
		if err != nil {
			unusable, unusableKey = err, k
			continue
		}
		var st stateData
		if plain, ok := g.open(v, c.Name, c.Value); ok && json.Unmarshal(plain, &st) == nil && st.State == state {
			return v, c.Name, st, nil
		}
	}
	if unusable != nil {
		return &view{providerKey: unusableKey}, "", stateData{}, unusable
	}
	return nil, "", stateData{}, nil
}

// parseCookie is the provider a cookie of kind names, in the gate's own form of the name.
func (g *Gate) parseCookie(name, kind string) (providerKey, bool) {
	if g.https {
		var ok bool
		if name, ok = strings.CutPrefix(name, hostPrefix); !ok {
			return providerKey{}, false
		}
	}
	rest, ok := strings.CutPrefix(name, kind+".")
	if !ok {
		return providerKey{}, false
	}
	ns, provider, ok := strings.Cut(rest, ".")
	if !ok || ns == "" || provider == "" {
		return providerKey{}, false
	}
	return providerKey{v1.NamespaceName(ns), v1.ObjectName(provider)}, true
}

// queryToken keeps an error code from the query to letters, digits and _, so the answer carries no markup.
func queryToken(s string) string {
	return strings.Map(func(c rune) rune {
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, s)
}

// exchange trades the code for the ID token. A refused client re-reads the client Secret once and exchanges again,
// so a sign-in that read a secret since rotated succeeds once the new one is stored (ADR-0336 Decision 4).
func (g *Gate) exchange(ctx context.Context, v *view, code, callback, verifier string) (*view, string, error) {
	raw, err := g.exchangeWith(ctx, v, code, callback, verifier)
	if !refusedClient(err) {
		return v, raw, err
	}
	fresh, lerr := g.load(ctx, v.ns, v.name)
	if lerr != nil {
		return v, "", errors.Join(err, lerr)
	}
	raw, err = g.exchangeWith(ctx, fresh, code, callback, verifier)
	return fresh, raw, err
}

func (g *Gate) exchangeWith(ctx context.Context, v *view, code, callback, verifier string) (string, error) {
	cfg, err := g.oauth2Config(v, callback)
	if err != nil {
		return "", err
	}
	tok, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, v.client), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", err
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return "", fault.Unauthorizedf(op, "the token endpoint answered no id_token")
	}
	return raw, nil
}

// refusedClient is a token endpoint's refusal of the client itself: invalid_client, or 401 (RFC 6749 §5.2).
func refusedClient(err error) bool {
	var re *oauth2.RetrieveError
	return errors.As(err, &re) && (re.ErrorCode == "invalid_client" || re.Response != nil && re.Response.StatusCode == http.StatusUnauthorized)
}

// verifyIDToken runs go-oidc's checks with the client ID, then azp and the nonce (ADR-0335 Decision 4), and returns
// the token's expiry.
func (g *Gate) verifyIDToken(ctx context.Context, v *view, raw, nonce string) (time.Time, error) {
	idt, err := g.verifier(v, v.clientID).Verify(ctx, raw)
	if err != nil {
		return time.Time{}, err
	}
	var extra struct {
		AuthorizedParty string `json:"azp"`
	}
	if err := idt.Claims(&extra); err != nil {
		return time.Time{}, err
	}
	if extra.AuthorizedParty != "" && extra.AuthorizedParty != v.clientID {
		return time.Time{}, fault.Unauthorizedf(op, "azp %q is not the client ID", extra.AuthorizedParty)
	}
	if idt.Nonce != nonce {
		return time.Time{}, fault.Unauthorizedf(op, "the nonce is not this sign-in's")
	}
	return idt.Expiry, nil
}

// firstPath is where a sign-in returns: the path it began at when it starts with / but not //, /\ or /.auth, else /.
func firstPath(p string) string {
	path, _, _ := strings.Cut(p, "?")
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `/\`) || v1.UnderAuthPath(path) {
		return "/"
	}
	return p
}

// logout clears every funcd session cookie the call carries, of either form, and redirects to / (ADR-0335 Decision 3).
func (g *Gate) logout(w http.ResponseWriter, r *http.Request) {
	for _, c := range r.Cookies() {
		if strings.HasPrefix(strings.TrimPrefix(c.Name, hostPrefix), sessionCookie+".") {
			http.SetCookie(w, &http.Cookie{Name: c.Name, Path: "/", MaxAge: -1, HttpOnly: true,
				Secure: strings.HasPrefix(c.Name, hostPrefix), SameSite: http.SameSiteLaxMode})
		}
	}
	http.Redirect(w, r, "/", http.StatusFound)
}
