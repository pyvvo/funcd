package store_test

import (
	"context"
	"testing"
	"time"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// xorEnc is a trivial reversible cipher standing in for the real at-rest
// encryptor (P-P/F15 supplies tink/envelope). It proves the store routes the
// configured kinds' values through the Encryptor seam.
type xorEnc struct{ key byte }

func (e xorEnc) Encrypt(_ context.Context, pt []byte) ([]byte, error) {
	return xorBytes(pt, e.key), nil
}
func (e xorEnc) Decrypt(_ context.Context, ct []byte) ([]byte, error) {
	return xorBytes(ct, e.key), nil
}

func xorBytes(b []byte, k byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[i] ^ k
	}
	return out
}

// scenario: secret-encrypted-at-rest — with WithEncryptor for Secret, a Secret's
// stored bytes are ciphertext (a non-encrypting reader cannot decode them) and a
// Get through the encrypting store round-trips the plaintext.
func TestScenario_SecretEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	eng := memory.New()
	s := store.New(eng, store.WithEncryptor([]v1.Kind{v1.KindSecret}, xorEnc{key: 0x5A}))

	obj, ok := v1.NewObject(v1.KindSecret)
	if !ok {
		t.Fatal("NewObject(Secret) returned false")
	}
	sec, _ := obj.(*v1.Secret)
	sec.Name = "db-pw"
	sec.Namespace = "default"
	sec.ResourceGroup = "rg1"
	sec.Spec.Data = map[string][]byte{"password": []byte("s3cr3t")}
	if _, err := s.Create(ctx, sec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Round-trip through the encrypting store returns the plaintext.
	got, err := s.Get(ctx, v1.KindSecret.GVK(), "default", "db-pw")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gs, _ := got.(*v1.Secret)
	if gs == nil || string(gs.Spec.Data["password"]) != "s3cr3t" {
		t.Fatalf("decrypt round-trip mismatch: %+v", got)
	}

	// At rest the bytes are ciphertext: a NON-encrypting store over the same
	// engine cannot decode the value (it is not plaintext JSON).
	plain := store.New(eng)
	if _, err := plain.Get(ctx, v1.KindSecret.GVK(), "default", "db-pw"); err == nil {
		t.Fatal("non-encrypting reader decoded ciphertext-at-rest; value was not encrypted")
	}

	// With no encryptor configured, a Config (not a Secret) is stored as plaintext
	// — confirming the seam is scoped to the named kinds, not blanket encryption.
	cfgObj, _ := v1.NewObject(v1.KindConfig)
	cfg, _ := cfgObj.(*v1.Config)
	cfg.Name = "plain"
	cfg.Namespace = "default"
	cfg.ResourceGroup = "rg1"
	cfg.Spec.Data = map[string]string{"k": "v"}
	if _, err := s.Create(ctx, cfg); err != nil {
		t.Fatalf("Create(config): %v", err)
	}
	if _, err := plain.Get(ctx, v1.KindConfig.GVK(), "default", "plain"); err != nil {
		t.Fatalf("non-encrypting reader should read the un-encrypted Config: %v", err)
	}
}

// scenario: input-not-mutated — Create/Update stamp server fields on the returned
// clone, never on the caller's input object (regression test for metadata aliasing).
func TestScenario_InputNotMutated(t *testing.T) {
	ctx := context.Background()
	s := store.New(memory.New())

	in, _ := v1.NewObject(v1.KindConfig)
	cfg, _ := in.(*v1.Config)
	cfg.Name = "imm"
	cfg.Namespace = "default"
	cfg.ResourceGroup = "rg1"
	cfg.Spec.Data = map[string]string{"k": "v"}

	created, err := s.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if cfg.UID != "" || cfg.Generation != 0 || cfg.ResourceVersion != "" {
		t.Fatalf("Create mutated the caller's input: uid=%q gen=%d rv=%q", cfg.UID, cfg.Generation, cfg.ResourceVersion)
	}
	cm := created.GetObjectMeta()
	if cm.UID == "" || cm.ResourceVersion == "" || cm.Generation != 1 {
		t.Fatalf("returned object missing server fields: uid=%q rv=%q gen=%d", cm.UID, cm.ResourceVersion, cm.Generation)
	}

	up, _ := v1.NewObject(v1.KindConfig)
	uc, _ := up.(*v1.Config)
	uc.Name = "imm"
	uc.Namespace = "default"
	uc.ResourceGroup = "rg1"
	uc.Spec.Data = map[string]string{"k": "v2"}
	uc.ResourceVersion = cm.ResourceVersion
	rvIn := uc.ResourceVersion
	updated, err := s.Update(ctx, uc)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if uc.UID != "" || uc.Generation != 0 || uc.ResourceVersion != rvIn {
		t.Fatalf("Update mutated the caller's input: uid=%q gen=%d rv=%q (rvIn=%q)", uc.UID, uc.Generation, uc.ResourceVersion, rvIn)
	}
	if updated.GetObjectMeta().ResourceVersion == rvIn {
		t.Fatal("Update did not advance the returned resourceVersion")
	}
}

// scenario: generation-bumps-on-spec-change (status-only arm) — a status-only Update
// on a StatusObject kind (Function: empty spec) must NOT bump generation.
func TestScenario_GenerationStatusOnlyNoBump(t *testing.T) {
	ctx := context.Background()
	s := store.New(memory.New())

	in, _ := v1.NewObject(v1.KindFunction)
	fn, _ := in.(*v1.Function)
	fn.Name = "f1"
	fn.Namespace = "default"
	fn.ResourceGroup = "rg1"
	created, err := s.Create(ctx, fn)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g := created.GetObjectMeta().Generation; g != 1 {
		t.Fatalf("create generation=%d want 1", g)
	}

	upd, _ := created.(*v1.Function)
	upd.Status.Phase = "Ready"
	upd.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue})
	after, err := s.Update(ctx, upd)
	if err != nil {
		t.Fatalf("Update(status): %v", err)
	}
	if g := after.GetObjectMeta().Generation; g != 1 {
		t.Fatalf("status-only update bumped generation to %d, want 1 (unchanged)", g)
	}
}

// scenario: returned-object-independent-of-watch — mutating the object returned by Create
// must not corrupt the object delivered on the watch stream (no shared pointee).
func TestScenario_ReturnedObjectIndependentOfWatch(t *testing.T) {
	ctx := context.Background()
	s := store.New(memory.New())
	w, err := s.Watch(ctx, v1.KindConfig.GVK(), store.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Stop()

	in, _ := v1.NewObject(v1.KindConfig)
	c, _ := in.(*v1.Config)
	c.Name = "ind"
	c.Namespace = "default"
	c.ResourceGroup = "rg1"
	c.Spec.Data = map[string]string{"k": "v"}
	created, err := s.Create(ctx, c)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var ev store.Event
	select {
	case ev = <-w.ResultChan():
	case <-time.After(2 * time.Second):
		t.Fatal("no watch event received")
	}

	// Mutate the returned object; the already-delivered watch event must be unaffected.
	created.(*v1.Config).Spec.Data["k"] = "MUTATED"
	if got := ev.Object.(*v1.Config).Spec.Data["k"]; got != "v" {
		t.Fatalf("watch event aliased the returned object: got %q, want v", got)
	}
}

// scenario: noop-write-coalesced — an Update whose object is byte-identical to the stored one
// (only the matched resourceVersion) is a no-op: no revision advance and NO Modified event, so a
// reconcile that observed no change does not self-trigger another (control-loop quiescence, ADR-0047).
func TestScenario_NoopWriteCoalesced(t *testing.T) {
	ctx := context.Background()
	s := store.New(memory.New())
	w, err := s.Watch(ctx, v1.KindFunction.GVK(), store.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Stop()

	in, _ := v1.NewObject(v1.KindFunction)
	fn, _ := in.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "q1", "default", "rg1"
	created, err := s.Create(ctx, fn)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// drain the Create (Added) event
	select {
	case <-w.ResultChan():
	case <-time.After(2 * time.Second):
		t.Fatal("no Added event")
	}
	rv0 := created.GetObjectMeta().ResourceVersion

	// Re-Update with the unchanged object: must be a no-op.
	after, err := s.Update(ctx, created)
	if err != nil {
		t.Fatalf("Update(no-op): %v", err)
	}
	if rv := after.GetObjectMeta().ResourceVersion; rv != rv0 {
		t.Fatalf("no-op Update advanced resourceVersion: %s -> %s (want unchanged)", rv0, rv)
	}
	select {
	case ev := <-w.ResultChan():
		t.Fatalf("no-op Update published a watch event (%s) — must coalesce to nothing", ev.Type)
	case <-time.After(300 * time.Millisecond):
		// expected: no event
	}
}

// scenario: real-write-still-events — a real change advances the revision and fires exactly one
// Modified event (ADR-0006 behaviour intact under coalescing).
func TestScenario_RealWriteStillEvents(t *testing.T) {
	ctx := context.Background()
	s := store.New(memory.New())
	w, err := s.Watch(ctx, v1.KindFunction.GVK(), store.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Stop()

	in, _ := v1.NewObject(v1.KindFunction)
	fn, _ := in.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "q2", "default", "rg1"
	created, err := s.Create(ctx, fn)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	select {
	case <-w.ResultChan(): // Added
	case <-time.After(2 * time.Second):
		t.Fatal("no Added event")
	}
	rv0 := created.GetObjectMeta().ResourceVersion

	upd, _ := created.(*v1.Function)
	upd.Status.Phase = "Ready" // a real status change
	after, err := s.Update(ctx, upd)
	if err != nil {
		t.Fatalf("Update(real): %v", err)
	}
	if rv := after.GetObjectMeta().ResourceVersion; rv == rv0 {
		t.Fatalf("real Update did not advance resourceVersion (stayed %s)", rv)
	}
	select {
	case ev := <-w.ResultChan():
		if ev.Type != store.Modified {
			t.Fatalf("real Update event type=%s want Modified", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("real Update published no Modified event")
	}
}
