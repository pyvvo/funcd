package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
)

func kvStore(name string, mvb int64, mkb int) *KVStore {
	ks := &KVStore{
		TypeMeta:   TypeMeta{APIVersion: KindKVStore.GVK().APIVersion(), Kind: KindKVStore},
		ObjectMeta: ObjectMeta{Name: ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       KVStoreSpec{MaxValueBytes: mvb, MaxKeyBytes: mkb},
	}
	return ks
}

// scenario (types): KVStore + Grant roundtrip — full JSON equality after marshal/unmarshal.
func TestKVStoreGrantRoundtrip(t *testing.T) {
	ks := kvStore("counters", 2048, 64)
	ks.Status = KVStoreStatus{Status: Status{Phase: PhaseReady}, GrantRefs: 2}
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
	if ks2.Status.Phase != PhaseReady || ks2.Status.GrantRefs != 2 {
		t.Errorf("KVStore status roundtrip mismatch: %+v", ks2.Status)
	}

	g := &Grant{
		TypeMeta:   TypeMeta{APIVersion: KindGrant.GVK().APIVersion(), Kind: KindGrant},
		ObjectMeta: ObjectMeta{Name: "g1", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       GrantSpec{Function: "counter", Binding: "counters", Store: "counters", Mode: KVModeRW},
	}
	gdata, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal Grant: %v", err)
	}
	var g2 Grant
	if err := json.Unmarshal(gdata, &g2); err != nil {
		t.Fatalf("unmarshal Grant: %v", err)
	}
	if g2.Spec != g.Spec {
		t.Errorf("Grant spec roundtrip mismatch: %+v vs %+v", g2.Spec, g.Spec)
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

// scenario (types): KVStore.Validate — envelope + non-negative caps.
func TestKVStoreValidate(t *testing.T) {
	if err := kvStore("ok", 0, 0).Validate(); err != nil {
		t.Errorf("valid KVStore rejected: %v", err)
	}
	if err := kvStore("neg", -1, 0).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("negative maxValueBytes: want Invalid, got %v", err)
	}
	if err := kvStore("neg2", 0, -1).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("negative maxKeyBytes: want Invalid, got %v", err)
	}
}

// scenario (types): Grant.Validate — mode oneof, binding DNS-1123, function/store non-empty.
func TestGrantValidate(t *testing.T) {
	base := func() *Grant {
		return &Grant{
			TypeMeta:   TypeMeta{APIVersion: KindGrant.GVK().APIVersion(), Kind: KindGrant},
			ObjectMeta: ObjectMeta{Name: "g1", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       GrantSpec{Function: "f", Binding: "b", Store: "s", Mode: KVModeRO},
		}
	}
	if err := base().Validate(); err != nil {
		t.Errorf("valid Grant rejected: %v", err)
	}

	g := base()
	g.Spec.Mode = "rwx"
	if fault.KindOf(g.Validate()) != fault.Invalid {
		t.Errorf("bad mode: want Invalid")
	}
	g = base()
	g.Spec.Binding = "Not_A_Label"
	if fault.KindOf(g.Validate()) != fault.Invalid {
		t.Errorf("bad binding: want Invalid")
	}
	g = base()
	g.Spec.Function = ""
	if fault.KindOf(g.Validate()) != fault.Invalid {
		t.Errorf("empty function: want Invalid")
	}
	g = base()
	g.Spec.Store = ""
	if fault.KindOf(g.Validate()) != fault.Invalid {
		t.Errorf("empty store: want Invalid")
	}
}
