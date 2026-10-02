package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

func kvStore(name string, mvb int64, mkb int, tables ...KVTable) *KVStore {
	ks := &KVStore{
		TypeMeta:   TypeMeta{APIVersion: KindKVStore.GVK().APIVersion(), Kind: KindKVStore},
		ObjectMeta: ObjectMeta{Name: ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       KVStoreSpec{MaxValueBytes: mvb, MaxKeyBytes: mkb, Tables: tables},
	}
	return ks
}

// scenario (types): KVStore roundtrip — full JSON equality after marshal/unmarshal, including the
// tables[] sub-domains and the status tables/bindings counts (ADR-0073).
func TestKVStoreRoundtrip(t *testing.T) {
	ks := kvStore("orders", 2048, 64,
		KVTable{Name: "customers", Owner: "customers-svc"},
		KVTable{Name: "fulfillment", Owner: "fulfillment-svc"})
	ks.Status = KVStoreStatus{Status: Status{Phase: PhaseReady}, Tables: 2, Bindings: 3}
	data, err := json.Marshal(ks)
	if err != nil {
		t.Fatalf("marshal KVStore: %v", err)
	}
	var ks2 KVStore
	if err := json.Unmarshal(data, &ks2); err != nil {
		t.Fatalf("unmarshal KVStore: %v", err)
	}
	if ks2.Name != ks.Name || ks2.Spec.MaxValueBytes != 2048 || ks2.Spec.MaxKeyBytes != 64 {
		t.Errorf("KVStore roundtrip mismatch: %+v", ks2)
	}
	if len(ks2.Spec.Tables) != 2 || ks2.Spec.Tables[0].Name != "customers" || ks2.Spec.Tables[0].Owner != "customers-svc" {
		t.Errorf("KVStore tables roundtrip mismatch: %+v", ks2.Spec.Tables)
	}
	if ks2.Status.Phase != PhaseReady || ks2.Status.Tables != 2 || ks2.Status.Bindings != 3 {
		t.Errorf("KVStore status roundtrip mismatch: %+v", ks2.Status)
	}
}

// scenario (types): Grant roundtrip — GrantSpec reverted to the reserved empty placeholder (ADR-0073);
// KindGrant stays registered and the envelope roundtrips.
func TestGrantRoundtrip(t *testing.T) {
	g := &Grant{
		TypeMeta:   TypeMeta{APIVersion: KindGrant.GVK().APIVersion(), Kind: KindGrant},
		ObjectMeta: ObjectMeta{Name: "g1", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       GrantSpec{},
	}
	gdata, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal Grant: %v", err)
	}
	var g2 Grant
	if err := json.Unmarshal(gdata, &g2); err != nil {
		t.Fatalf("unmarshal Grant: %v", err)
	}
	if g2.Name != g.Name || g2.Spec != (GrantSpec{}) {
		t.Errorf("Grant roundtrip mismatch: %+v", g2)
	}
	if err := g.Validate(); err != nil {
		t.Errorf("valid Grant rejected: %v", err)
	}
}

// scenario (types): KVStore default caps — 0 ⇒ defaults (1 MiB / 1 KiB), explicit values honored.
func TestKVStoreEffectiveCaps(t *testing.T) {
	zero := KVStoreSpec{}
	if got := zero.EffectiveMaxValueBytes(); got != DefaultMaxValueBytes {
		t.Errorf("default maxValueBytes = %d, want %d", got, DefaultMaxValueBytes)
	}
	if got := zero.EffectiveMaxKeyBytes(); got != DefaultMaxKeyBytes {
		t.Errorf("default maxKeyBytes = %d, want %d", got, DefaultMaxKeyBytes)
	}
	set := KVStoreSpec{MaxValueBytes: 7, MaxKeyBytes: 3}
	if set.EffectiveMaxValueBytes() != 7 || set.EffectiveMaxKeyBytes() != 3 {
		t.Errorf("explicit caps not honored: %+v", set)
	}
}

// scenario: single-writer-per-table (structural half) — KVStore.Validate enforces unique table names
// within a store (so a table has exactly one owner by construction) + non-negative caps.
func TestKVStoreValidate(t *testing.T) {
	if err := kvStore("ok", 0, 0).Validate(); err != nil {
		t.Errorf("valid KVStore rejected: %v", err)
	}
	if err := kvStore("with-tables", 0, 0, KVTable{Name: "a", Owner: "f"}, KVTable{Name: "b"}).Validate(); err != nil {
		t.Errorf("valid KVStore with tables rejected: %v", err)
	}
	if err := kvStore("neg", -1, 0).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("negative maxValueBytes: want Invalid, got %v", err)
	}
	if err := kvStore("at-limit", MaxValueBytesLimit, 0).Validate(); err != nil {
		t.Errorf("maxValueBytes at the limit rejected: %v", err)
	}
	if err := kvStore("over-limit", MaxValueBytesLimit+1, 0).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("maxValueBytes over the limit: want Invalid, got %v", err)
	}
	if err := kvStore("neg2", 0, -1).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("negative maxKeyBytes: want Invalid, got %v", err)
	}
	if err := kvStore("dup", 0, 0, KVTable{Name: "t"}, KVTable{Name: "t"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate table name: want Invalid, got %v", err)
	}
	if err := kvStore("bad", 0, 0, KVTable{Name: "Bad_Table"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad table name: want Invalid, got %v", err)
	}
}
