// Package oidcprovider is an OpenID Connect provider for tests (ADR-0335, ADR-0336): discovery, the authorization code
// flow with PKCE, ID tokens signed with RS256 or EdDSA, its keys, and client registration (RFC 7591) with its upkeep
// (RFC 7592). A test signs a user in, mints a token, rotates the keys, refuses a call, keeps a secret on an update,
// purges every client, and snapshots and restores the clients as a Backup and a Restore of the engine would.
package oidcprovider

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

// The operations a test refuses or counts.
const (
	OpRegister = "register"
	OpRead     = "read"
	OpUpdate   = "update"
	OpToken    = "token"
	OpKeys     = "keys"
)

// Alg is a signing algorithm.
type Alg = jose.SignatureAlgorithm

// RS256 and EdDSA are the algorithms the provider signs with.
const (
	RS256 = jose.RS256
	EdDSA = jose.EdDSA
)

// rsaBits is the RS256 keys' size.
const rsaBits = 2048

// tokenTTL is how long an ID token lives unless the test sets its expiry.
const tokenTTL = 30 * time.Minute

// Options shape the provider.
type Options struct {
	// Path is the issuer's path under the server, as rauthy's /auth/v1.
	Path string
	// Public is the base URL the provider names itself with, its endpoints included, as an add-on engine names its
	// public host; "" is the server's own URL.
	Public string
	// Plain serves http, as an add-on engine at its own address does; otherwise the provider serves https.
	Plain bool
	// RegistrationToken, when set, is the Bearer token a registration (POST) must carry.
	RegistrationToken string
}

// Claims are an ID token's claims.
type Claims struct {
	Issuer            string   `json:"iss,omitempty"`
	Subject           string   `json:"sub,omitempty"`
	Audience          []string `json:"aud,omitempty"`
	Expiry            int64    `json:"exp,omitempty"`
	IssuedAt          int64    `json:"iat,omitempty"`
	Nonce             string   `json:"nonce,omitempty"`
	AuthorizedParty   string   `json:"azp,omitempty"`
	Name              string   `json:"name,omitempty"`
	PreferredUsername string   `json:"preferred_username,omitempty"`
	Email             string   `json:"email,omitempty"`
	Padding           string   `json:"padding,omitempty"`
}

// Client is a client the provider knows.
type Client struct {
	ID                string   `json:"client_id"`
	Secret            string   `json:"client_secret"`
	RegistrationToken string   `json:"registration_access_token,omitempty"`
	RegistrationURI   string   `json:"registration_client_uri,omitempty"`
	RedirectURIs      []string `json:"redirect_uris"`
	GrantTypes        []string `json:"grant_types,omitempty"`
	AuthMethod        string   `json:"token_endpoint_auth_method,omitempty"`
	Name              string   `json:"client_name,omitempty"`
}

// Snapshot is the provider's clients at one time.
type Snapshot map[string]Client

type key struct {
	kid    string
	alg    jose.SignatureAlgorithm
	signer crypto.Signer
}

type grant struct {
	client, redirect, nonce, challenge string
	claims                             Claims
}

// Provider is the provider and its server.
type Provider struct {
	t      testing.TB
	srv    *httptest.Server
	opts   Options
	issuer string

	mu          sync.Mutex
	keys        []key
	signWith    jose.SignatureAlgorithm
	user        Claims
	clients     map[string]Client
	codes       map[string]grant
	refuse      map[string]int
	calls       map[string]int
	keepSecret  bool
	maskOnRead  bool
	deny        string
	idTokenEdit func(*Claims)
	beforeToken func()
	sequence    int
}

// New starts the provider; the test's cleanup stops it.
func New(t testing.TB, opts Options) *Provider {
	t.Helper()
	p := &Provider{t: t, opts: opts, signWith: RS256, clients: map[string]Client{}, codes: map[string]grant{},
		refuse: map[string]int{}, calls: map[string]int{},
		user: Claims{Subject: "user-1", Name: "Ada Lovelace", PreferredUsername: "ada", Email: "ada@example.com"}}
	p.AddKey(RS256)
	p.AddKey(EdDSA)
	mux := http.NewServeMux()
	base := strings.TrimSuffix(opts.Path, "/")
	mux.HandleFunc("GET "+base+"/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET "+base+"/jwks", p.jwks)
	mux.HandleFunc("GET "+base+"/authorize", p.authorize)
	mux.HandleFunc("POST "+base+"/token", p.token)
	mux.HandleFunc("POST "+base+"/register", p.register)
	mux.HandleFunc("GET "+base+"/register/{id}", p.read)
	mux.HandleFunc("PUT "+base+"/register/{id}", p.update)
	if opts.Plain {
		p.srv = httptest.NewServer(mux)
	} else {
		p.srv = httptest.NewTLSServer(mux)
	}
	t.Cleanup(p.srv.Close)
	public := strings.TrimSuffix(opts.Public, "/")
	if public == "" {
		public = p.srv.URL
	}
	p.issuer = public + base
	return p
}

// Issuer is the issuer URL the provider names itself with.
func (p *Provider) Issuer() string { return p.issuer }

// Addr is the server's own address, host:port.
func (p *Provider) Addr() string { return p.srv.Listener.Addr().String() }

// URL is the server's own URL.
func (p *Provider) URL() string { return p.srv.URL }

// Transport reaches the server, trusting its certificate.
func (p *Provider) Transport() http.RoundTripper { return p.srv.Client().Transport }

// SetUser sets the claims of the user who signs in next.
func (p *Provider) SetUser(c Claims) { p.mu.Lock(); p.user = c; p.mu.Unlock() }

// SignWith makes the ID tokens signed with alg.
func (p *Provider) SignWith(alg jose.SignatureAlgorithm) {
	p.mu.Lock()
	p.signWith = alg
	p.mu.Unlock()
}

// EditIDTokens changes each ID token's claims before they are signed; nil stops it.
func (p *Provider) EditIDTokens(edit func(*Claims)) { p.mu.Lock(); p.idTokenEdit = edit; p.mu.Unlock() }

// BeforeNextToken runs f once, at the start of the next token request, before its client is checked.
func (p *Provider) BeforeNextToken(f func()) { p.mu.Lock(); p.beforeToken = f; p.mu.Unlock() }

// DenyNext makes the next authorization answer the client with this error code.
func (p *Provider) DenyNext(code string) { p.mu.Lock(); p.deny = code; p.mu.Unlock() }

// Refuse makes every call of op answer status; 0 serves it again.
func (p *Provider) Refuse(op string, status int) { p.mu.Lock(); p.refuse[op] = status; p.mu.Unlock() }

// KeepSecretOnUpdate makes an update keep the client's secret, as a provider that refuses a rotation does.
func (p *Provider) KeepSecretOnUpdate(keep bool) { p.mu.Lock(); p.keepSecret = keep; p.mu.Unlock() }

// MaskSecretOnRead makes a read answer the secret masked.
func (p *Provider) MaskSecretOnRead(mask bool) { p.mu.Lock(); p.maskOnRead = mask; p.mu.Unlock() }

// Calls is how many calls of op the provider answered or refused.
func (p *Provider) Calls(op string) int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls[op] }

// Clients are the clients the provider knows, by ID.
func (p *Provider) Clients() Snapshot { p.mu.Lock(); defer p.mu.Unlock(); return maps.Clone(p.clients) }

// Purge forgets every client, as an idle purge does.
func (p *Provider) Purge() { p.mu.Lock(); p.clients = map[string]Client{}; p.mu.Unlock() }

// Snapshot copies the clients, as a Backup of the engine does.
func (p *Provider) Snapshot() Snapshot { return p.Clients() }

// Restore puts a snapshot's clients back, as a Restore of the engine does.
func (p *Provider) Restore(s Snapshot) { p.mu.Lock(); p.clients = maps.Clone(s); p.mu.Unlock() }

// AddClient makes a client by hand, as for a credentialsSecret.
func (p *Provider) AddClient(id, secret string, redirects ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients[id] = Client{ID: id, Secret: secret, RedirectURIs: redirects, AuthMethod: "client_secret_post"}
}

// AddKey adds a signing key of alg and makes it the one alg signs with; it returns its kid.
func (p *Provider) AddKey(alg jose.SignatureAlgorithm) string {
	p.t.Helper()
	var signer crypto.Signer
	var err error
	switch alg {
	case EdDSA:
		_, signer, err = ed25519.GenerateKey(rand.Reader)
	default:
		signer, err = rsa.GenerateKey(rand.Reader, rsaBits)
	}
	require.NoError(p.t, err)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sequence++
	k := key{kid: string(alg) + "-" + strings.Repeat("k", p.sequence), alg: alg, signer: signer}
	p.keys = append(p.keys, k)
	return k.kid
}

// Token signs claims with the newest key of alg.
func (p *Provider) Token(alg jose.SignatureAlgorithm, c Claims) string {
	p.t.Helper()
	p.mu.Lock()
	var k key
	for _, cand := range p.keys {
		if cand.alg == alg {
			k = cand
		}
	}
	p.mu.Unlock()
	require.NotNil(p.t, k.signer, "no key of %s", alg)
	return p.sign(k, c)
}

// TokenWithForeignKey signs claims with a key the provider does not publish, under the kid of one it does.
func (p *Provider) TokenWithForeignKey(c Claims) string {
	p.t.Helper()
	foreign, err := rsa.GenerateKey(rand.Reader, rsaBits)
	require.NoError(p.t, err)
	p.mu.Lock()
	kid := p.keys[0].kid
	p.mu.Unlock()
	return p.sign(key{kid: kid, alg: RS256, signer: foreign}, c)
}

// Claims are the claims of an ID token the provider issues to client now, for the user.
func (p *Provider) Claims(client, nonce string) Claims {
	p.mu.Lock()
	c := p.user
	p.mu.Unlock()
	now := time.Now()
	c.Issuer, c.Audience, c.Nonce, c.IssuedAt, c.Expiry = p.issuer, []string{client}, nonce, now.Unix(), now.Add(tokenTTL).Unix()
	return c
}

func (p *Provider) sign(k key, c Claims) string {
	p.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: k.alg, Key: k.signer},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), k.kid))
	require.NoError(p.t, err)
	payload, err := json.Marshal(c)
	require.NoError(p.t, err)
	jws, err := signer.Sign(payload)
	require.NoError(p.t, err)
	out, err := jws.CompactSerialize()
	require.NoError(p.t, err)
	return out
}

// counted counts a call of op and answers the refusal a test set, reporting whether it did.
func (p *Provider) counted(w http.ResponseWriter, op string) bool {
	p.mu.Lock()
	p.calls[op]++
	status := p.refuse[op]
	p.mu.Unlock()
	if status != 0 {
		http.Error(w, `{"error":"refused by the test","secret_detail":"do-not-echo"}`, status)
		return true
	}
	return false
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Issuer        string   `json:"issuer"`
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		JWKS          string   `json:"jwks_uri"`
		Registration  string   `json:"registration_endpoint"`
		Algs          []string `json:"id_token_signing_alg_values_supported"`
		AuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
		Responses     []string `json:"response_types_supported"`
		Subjects      []string `json:"subject_types_supported"`
	}{p.issuer, p.issuer + "/authorize", p.issuer + "/token", p.issuer + "/jwks", p.issuer + "/register",
		[]string{string(RS256), string(EdDSA)}, []string{"client_secret_post", "client_secret_basic"}, []string{"code"},
		[]string{"public"}})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	if p.counted(w, OpKeys) {
		return
	}
	p.mu.Lock()
	set := jose.JSONWebKeySet{}
	for _, k := range p.keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: k.signer.Public(), KeyID: k.kid, Algorithm: string(k.alg), Use: "sig"})
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, set)
}

// authorize signs the user in at once and sends the browser back with a code, or with the error DenyNext set.
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	c, known := p.clients[q.Get("client_id")]
	deny := p.deny
	p.deny = ""
	p.mu.Unlock()
	redirect := q.Get("redirect_uri")
	if !known || !slices.Contains(c.RedirectURIs, redirect) {
		http.Error(w, "unknown client or redirect_uri", http.StatusBadRequest)
		return
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		!slices.Contains(strings.Fields(q.Get("scope")), "openid") {
		http.Error(w, "the request is not a code flow with PKCE S256", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(redirect)
	require.NoError(p.t, err)
	v := url.Values{"state": {q.Get("state")}}
	if deny != "" {
		v.Set("error", deny)
	} else {
		code, claims := random(), p.Claims(c.ID, q.Get("nonce"))
		p.mu.Lock()
		p.codes[code] = grant{client: c.ID, redirect: redirect, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"),
			claims: claims}
		p.mu.Unlock()
		v.Set("code", code)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// token redeems a code: the client's secret is checked first, so a code refused for the secret stays redeemable.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	before := p.beforeToken
	p.beforeToken = nil
	p.mu.Unlock()
	if before != nil {
		before()
	}
	if p.counted(w, OpToken) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, oauthError{"invalid_request"})
		return
	}
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	c, known := p.clients[id]
	if !known || c.Secret != secret {
		writeJSON(w, http.StatusUnauthorized, oauthError{"invalid_client"})
		return
	}
	g, ok := p.codes[r.PostForm.Get("code")]
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || g.client != id || g.redirect != r.PostForm.Get("redirect_uri") ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || r.PostForm.Get("grant_type") != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, oauthError{"invalid_grant"})
		return
	}
	delete(p.codes, r.PostForm.Get("code"))
	claims := g.claims
	if p.idTokenEdit != nil {
		p.idTokenEdit(&claims)
	}
	var k key
	for _, cand := range p.keys {
		if cand.alg == p.signWith {
			k = cand
		}
	}
	idToken := p.sign(k, claims)
	writeJSON(w, http.StatusOK, struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		IDToken     string `json:"id_token"`
	}{random(), "Bearer", int(tokenTTL / time.Second), idToken})
}

type oauthError struct {
	Error string `json:"error"`
}

// register makes a client (RFC 7591 §3).
func (p *Provider) register(w http.ResponseWriter, r *http.Request) {
	if p.counted(w, OpRegister) {
		return
	}
	if t := p.opts.RegistrationToken; t != "" && r.Header.Get("Authorization") != "Bearer "+t {
		writeJSON(w, http.StatusUnauthorized, oauthError{"invalid_token"})
		return
	}
	var c Client
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || len(c.RedirectURIs) == 0 {
		writeJSON(w, http.StatusBadRequest, oauthError{"invalid_client_metadata"})
		return
	}
	c.ID, c.Secret, c.RegistrationToken = "dyn$"+random(), random(), random()
	c.RegistrationURI = p.issuer + "/register/" + url.PathEscape(c.ID)
	p.mu.Lock()
	p.clients[c.ID] = c
	p.mu.Unlock()
	writeJSON(w, http.StatusCreated, c)
}

// client is the client of a read or an update, after its registration token is checked; ok is false once answered.
func (p *Provider) client(w http.ResponseWriter, r *http.Request) (Client, bool) {
	p.mu.Lock()
	c, known := p.clients[r.PathValue("id")]
	p.mu.Unlock()
	switch {
	case !known:
		writeJSON(w, http.StatusUnauthorized, oauthError{"invalid_token"})
		return Client{}, false
	case r.Header.Get("Authorization") != "Bearer "+c.RegistrationToken:
		writeJSON(w, http.StatusUnauthorized, oauthError{"invalid_token"})
		return Client{}, false
	}
	return c, true
}

// read answers the client with its current secret and redirect URIs (RFC 7592 §2.1).
func (p *Provider) read(w http.ResponseWriter, r *http.Request) {
	if p.counted(w, OpRead) {
		return
	}
	c, ok := p.client(w, r)
	if !ok {
		return
	}
	p.mu.Lock()
	if p.maskOnRead {
		c.Secret = "********"
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, c)
}

// update replaces the client's metadata and issues a new secret, refusing the old one at once, unless the test keeps it
// (RFC 7592 §2.2).
func (p *Provider) update(w http.ResponseWriter, r *http.Request) {
	if p.counted(w, OpUpdate) {
		return
	}
	c, ok := p.client(w, r)
	if !ok {
		return
	}
	var next Client
	if err := json.NewDecoder(r.Body).Decode(&next); err != nil || next.ID != c.ID || len(next.RedirectURIs) == 0 {
		writeJSON(w, http.StatusBadRequest, oauthError{"invalid_client_metadata"})
		return
	}
	p.mu.Lock()
	c.RedirectURIs, c.GrantTypes, c.AuthMethod, c.Name = next.RedirectURIs, next.GrantTypes, next.AuthMethod, next.Name
	if !p.keepSecret {
		c.Secret = random()
	}
	p.clients[c.ID] = c
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, c)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
