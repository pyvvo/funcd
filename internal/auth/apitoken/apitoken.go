// Package apitoken is the API tokens kept in the metastore (ADR-0370). An admin mints a token for a
// declared APIToken; funcd shows it once and keeps the SHA-256 of its secret with an expiry. The API and the edge of
// every node check it against their own store, so a token works, changes and ends with no restart.
package apitoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/store"
)

// prefix starts every token, funcd.<name>.<secret>: the name finds the APIToken with one read, as an Identity's lake
// token carries its owner (ADR-0153).
const prefix = "funcd."

// The bounds of a minted token's life.
const (
	defaultTTL = 30 * 24 * time.Hour
	minTTL     = time.Second
	maxTTL     = 365 * 24 * time.Hour
)

// Minter mints the tokens of the APITokens declared in a store.
type Minter struct {
	st  store.Store
	now func() time.Time
}

// NewMinter mints over st; now nil is time.Now.
func NewMinter(st store.Store, now func() time.Time) *Minter {
	if now == nil {
		now = time.Now
	}
	return &Minter{st: st, now: now}
}

// MintAPIToken mints a token for the APIToken name, valid for ttl (0 ⇒ 30 days, at most 365 days). Its hash replaces the
// one before, so an earlier token of name stops working.
func (m *Minter) MintAPIToken(ctx context.Context, name v1.ObjectName, ttl time.Duration) (string, time.Time, error) {
	const op = "apitoken.Mint"
	switch {
	case ttl == 0:
		ttl = defaultTTL
	case ttl < minTTL || ttl > maxTTL:
		return "", time.Time{}, fault.Invalidf(op, "ttl %s is not from 1s to %s", ttl, maxTTL)
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", time.Time{}, fault.Wrapf(err, fault.Internal, op, "draw the token's secret")
	}
	secret := base64.RawURLEncoding.EncodeToString(b[:])
	now := m.now()
	expires := now.Add(ttl)
	for range 5 {
		obj, err := m.st.Get(ctx, v1.KindAPIToken.GVK(), "", name)
		if err != nil {
			return "", time.Time{}, err
		}
		t, ok := obj.(*v1.APIToken)
		if !ok {
			return "", time.Time{}, fault.Internalf(op, "APIToken %s is a %T", name, obj)
		}
		t.Status = v1.APITokenStatus{TokenHash: hash(secret), IssuedAt: v1.NewTimestamp(now), ExpiresAt: v1.NewTimestamp(expires)}
		_, err = m.st.Update(ctx, t)
		if fault.KindOf(err) == fault.Conflict {
			continue
		}
		if err != nil {
			return "", time.Time{}, err
		}
		return prefix + string(name) + "." + secret, expires, nil
	}
	return "", time.Time{}, fault.Conflictf(op, "APIToken %s kept changing", name)
}

// Credentials checks tokens against the APITokens of a store (a middleware.CredentialStore).
type Credentials struct {
	st  store.Store
	now func() time.Time
}

// NewCredentials checks against st; now nil is time.Now.
func NewCredentials(st store.Store, now func() time.Time) Credentials {
	if now == nil {
		now = time.Now
	}
	return Credentials{st: st, now: now}
}

// Lookup resolves token to its caller. An unknown token, a wrong secret and an expired token are fault.Unauthorized.
func (c Credentials) Lookup(ctx context.Context, token string) (auth.Identity, error) {
	const op = "controlplane.authn"
	unknown := fault.Unauthorizedf(op, "unknown credential")
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok {
		return auth.Identity{}, unknown
	}
	name, secret, ok := strings.Cut(rest, ".")
	if !ok || name == "" || secret == "" {
		return auth.Identity{}, unknown
	}
	obj, err := c.st.Get(ctx, v1.KindAPIToken.GVK(), "", v1.ObjectName(name))
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return auth.Identity{}, unknown
	case err != nil:
		return auth.Identity{}, err
	}
	t, ok := obj.(*v1.APIToken)
	if !ok || t.Status.TokenHash == "" || subtle.ConstantTimeCompare([]byte(hash(secret)), []byte(t.Status.TokenHash)) != 1 {
		return auth.Identity{}, unknown
	}
	if !c.now().Before(time.Time(t.Status.ExpiresAt)) {
		return auth.Identity{}, fault.Unauthorizedf(op, "API token %s expired: mint a new one", name)
	}
	return auth.Identity{Subject: "apitoken/" + name, Role: auth.Role(t.Spec.Role), Namespaces: t.Spec.Namespaces}, nil
}

func hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
