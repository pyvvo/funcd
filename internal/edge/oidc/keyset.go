package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/sync/singleflight"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// The gate keeps a provider's signing keys for keyMaxAge at most, and fetches them for a token that names an unknown
// kid no sooner than keyRefetchInterval after the last fetch (ADR-0335 Decision 7).
const (
	keyMaxAge          = 24 * time.Hour
	keyRefetchInterval = time.Minute
)

// asymmetricAlgs are the algorithms a provider may sign with (ADR-0335 Decision 4): RS, PS, ES and EdDSA.
func asymmetricAlgs() []jose.SignatureAlgorithm {
	return []jose.SignatureAlgorithm{jose.RS256, jose.RS384, jose.RS512, jose.PS256, jose.PS384, jose.PS512,
		jose.ES256, jose.ES384, jose.ES512, jose.EdDSA}
}

// keySet is a provider's signing keys behind go-oidc's KeySet, fetched from its jwks_uri one fetch at a time: go-oidc's
// RemoteKeySet fetches for each unknown kid, this one at most once each keyRefetchInterval.
type keySet struct {
	client *http.Client
	url    string
	clock  clock.Clock
	group  singleflight.Group

	mu      sync.Mutex
	keys    *jose.JSONWebKeySet
	fetched time.Time
}

func newKeySet(client *http.Client, url string, c clock.Clock) *keySet {
	return &keySet{client: client, url: url, clock: c}
}

// VerifySignature checks raw's signature with the provider's keys and returns its payload; go-oidc checks the rest.
func (k *keySet) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	const op = "oidc.keySet.VerifySignature"
	jws, err := jose.ParseSigned(raw, asymmetricAlgs())
	if err != nil {
		return nil, fault.Unauthorizedf(op, "the token is not a JWS signed with an asymmetric algorithm")
	}
	if len(jws.Signatures) != 1 {
		return nil, fault.Unauthorizedf(op, "the token carries %d signatures, not one", len(jws.Signatures))
	}
	kid := jws.Signatures[0].Header.KeyID
	keys, err := k.current(ctx)
	if err != nil {
		return nil, err
	}
	if payload, found, err := verifyWith(jws, keys, kid); found {
		return payload, err
	}
	if keys, err = k.unknownKey(ctx); err != nil {
		return nil, err
	}
	if payload, found, err := verifyWith(jws, keys, kid); found {
		return payload, err
	}
	return nil, fault.Unauthorizedf(op, "the token's key %q is not one of the provider's", kid)
}

// current is the keys, fetched again when there are none or they are older than keyMaxAge.
func (k *keySet) current(ctx context.Context) (*jose.JSONWebKeySet, error) {
	k.mu.Lock()
	keys, fetched := k.keys, k.fetched
	k.mu.Unlock()
	if keys != nil && k.clock.Now().Sub(fetched) < keyMaxAge {
		return keys, nil
	}
	return k.fetch(ctx)
}

// unknownKey is the keys fetched again for a kid they lack, unless the last fetch is under keyRefetchInterval old.
func (k *keySet) unknownKey(ctx context.Context) (*jose.JSONWebKeySet, error) {
	k.mu.Lock()
	keys, fetched := k.keys, k.fetched
	k.mu.Unlock()
	if keys != nil && k.clock.Now().Sub(fetched) < keyRefetchInterval {
		return keys, nil
	}
	return k.fetch(ctx)
}

// fetch reads the keys once for every caller waiting on it, under a context no caller's end cancels. A caller that
// decided to fetch just before another fetch ended gets that fetch's keys: the keys are read at most once each
// keyRefetchInterval (ADR-0335 Decision 7).
func (k *keySet) fetch(ctx context.Context) (*jose.JSONWebKeySet, error) {
	ctx = context.WithoutCancel(ctx)
	v, err, _ := k.group.Do(k.url, func() (interface{}, error) {
		k.mu.Lock()
		keys, fetched := k.keys, k.fetched
		k.mu.Unlock()
		if keys != nil && k.clock.Now().Sub(fetched) < keyRefetchInterval {
			return keys, nil
		}
		body, err := get(ctx, k.client, k.url)
		if err != nil {
			return nil, err
		}
		var set jose.JSONWebKeySet
		if err := json.Unmarshal(body, &set); err != nil {
			return nil, fault.Wrapf(err, fault.Unavailable, "oidc.keySet.fetch", "decode the keys at %s", k.url)
		}
		k.mu.Lock()
		k.keys, k.fetched = &set, k.clock.Now()
		k.mu.Unlock()
		return &set, nil
	})
	if err != nil {
		return nil, err
	}
	keys, _ := v.(*jose.JSONWebKeySet)
	return keys, nil
}

// verifyWith checks jws with the keys of kid, or every key when it names none: found is false when no key is a
// candidate, so the caller may fetch the keys again.
func verifyWith(jws *jose.JSONWebSignature, keys *jose.JSONWebKeySet, kid string) ([]byte, bool, error) {
	candidates := keys.Keys
	if kid != "" {
		candidates = keys.Key(kid)
	}
	found := false
	for _, key := range candidates {
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		found = true
		if payload, err := jws.Verify(key); err == nil {
			return payload, true, nil
		}
	}
	if !found {
		return nil, false, nil
	}
	return nil, true, fault.Unauthorizedf("oidc.keySet.VerifySignature", "the token's signature does not verify")
}
