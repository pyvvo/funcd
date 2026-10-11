package v1alpha1

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

func ordersNightly(mutate func(*BackupSchedule)) *BackupSchedule {
	s := &BackupSchedule{
		TypeMeta:   typeMetaFor(KindBackupSchedule),
		ObjectMeta: ObjectMeta{Name: "orders-nightly", Namespace: "orders", ResourceGroup: "rg"},
		Spec: BackupScheduleSpec{
			Schedule: "0 3 * * *", TimeZone: "Europe/Paris",
			Scope:      BackupScope{KVStores: []ObjectName{"orders-state"}, Buckets: []ObjectName{"orders-files"}},
			Target:     BackupTarget{URL: "s3://orders-backup?region=eu-west-3", CredentialsSecret: "backup-writer"},
			Encryption: BackupEncryption{Recipients: []string{"age1owner", "age1recovery"}},
		},
	}
	if mutate != nil {
		mutate(s)
	}
	return s
}

// TestBackupNameFromSlot (ADR-0222 Decision 3): a 54-character schedule name and a slot up to 2160 give a 63-character
// Backup name; 55 characters are refused, as is a 57-character Restore, whose export adds "-before".
func TestBackupNameFromSlot(t *testing.T) {
	slot := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	if got := BackupSlotName("orders-nightly", slot); got != ObjectName("orders-nightly-"+itoa(slot.Unix()/60)) {
		t.Fatalf("BackupSlotName = %q", got)
	}
	name := ObjectName(strings.Repeat("a", 54))
	late := time.Date(2159, 12, 31, 23, 59, 0, 0, time.UTC)
	if got := BackupSlotName(name, late); len(got) != 63 || got.Validate() != nil {
		t.Fatalf("a 54-character schedule at %s: %q (%d characters)", late, got, len(got))
	}
	if err := ordersNightly(func(s *BackupSchedule) { s.Name = name }).Validate(); err != nil {
		t.Fatalf("54 characters: %v", err)
	}
	err := ordersNightly(func(s *BackupSchedule) { s.Name = name + "a" }).Validate()
	if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "longer than 54") {
		t.Fatalf("55 characters: %v", err)
	}
	r := &Restore{TypeMeta: typeMetaFor(KindRestore), ObjectMeta: ObjectMeta{Name: ObjectName(strings.Repeat("r", 56)),
		Namespace: "orders", ResourceGroup: "rg"}, Spec: RestoreSpec{Backup: "b", Source: RestoreSource{CredentialsSecret: "c",
		IdentitySecret: "i"}, Members: []RestoreMember{{Kind: KindKVStore, Name: "s", InPlace: true}}}}
	if err := r.Validate(); err != nil || len(RestoreExportName(r.Name)) != 63 {
		t.Fatalf("56 characters: %v, export %q", err, RestoreExportName(r.Name))
	}
	r.Name += "r"
	if err := r.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("57 characters: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestBackupScheduleValidate(t *testing.T) {
	cases := map[string]struct {
		mutate func(*BackupSchedule)
		want   string
	}{
		"valid":            {func(*BackupSchedule) {}, ""},
		"bad cron":         {func(s *BackupSchedule) { s.Spec.Schedule = "61 * * * *" }, "spec.schedule"},
		"bad zone":         {func(s *BackupSchedule) { s.Spec.TimeZone = "Mars/Base" }, "spec.timeZone"},
		"no scope":         {func(s *BackupSchedule) { s.Spec.Scope = BackupScope{} }, "sets 0 forms"},
		"two forms":        {func(s *BackupSchedule) { s.Spec.Scope.App = "orders" }, "sets 2 forms"},
		"repeated name":    {func(s *BackupSchedule) { s.Spec.Scope.KVStores = []ObjectName{"a", "a"} }, "kvStores[1] repeats"},
		"file target":      {func(s *BackupSchedule) { s.Spec.Target.URL = "file:///srv/backup" }, "spec.target.url"},
		"no secret":        {func(s *BackupSchedule) { s.Spec.Target.CredentialsSecret = "" }, "credentialsSecret"},
		"one recipient":    {func(s *BackupSchedule) { s.Spec.Encryption.Recipients = []string{"age1owner"} }, "want at least 2"},
		"same recipient":   {func(s *BackupSchedule) { s.Spec.Encryption.Recipients = []string{"age1a", "age1a"} }, "repeats"},
		"negative ttl":     {func(s *BackupSchedule) { s.Spec.TTL = -1 }, "spec.ttl"},
		"resource group":   {func(s *BackupSchedule) { s.Spec.Scope = BackupScope{ResourceGroup: "rg"} }, ""},
		"app scope":        {func(s *BackupSchedule) { s.Spec.Scope = BackupScope{App: "orders"} }, ""},
		"catalog by name":  {func(s *BackupSchedule) { s.Spec.Scope = BackupScope{Catalogs: []ObjectName{"lake"}} }, ""},
		"bad catalog name": {func(s *BackupSchedule) { s.Spec.Scope = BackupScope{Catalogs: []ObjectName{"Lake"}} }, "catalogs[0]"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := ordersNightly(c.mutate).Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate = %v, want Invalid naming %q", err, c.want)
			}
		})
	}
	if ordersNightly(nil).Spec.EffectiveTTL() != DefaultBackupTTL {
		t.Fatal("an unset ttl is 720h")
	}
}

func TestRestoreValidate(t *testing.T) {
	base := func(ms ...RestoreMember) *Restore {
		return &Restore{TypeMeta: typeMetaFor(KindRestore), ObjectMeta: ObjectMeta{Name: "r", Namespace: "orders", ResourceGroup: "rg"},
			Spec: RestoreSpec{Backup: "b", Source: RestoreSource{CredentialsSecret: "reader", IdentitySecret: "owner-key"}, Members: ms}}
	}
	for name, c := range map[string]struct {
		r    *Restore
		want string
	}{
		"as":              {base(RestoreMember{Kind: KindKVStore, Name: "s", As: "s-r"}), ""},
		"asCatalog":       {base(RestoreMember{Kind: KindCatalogService, Name: "lake", AsCatalog: &CatalogRef{Bucket: "b", Prefix: "p"}}), ""},
		"no member":       {base(), "spec.members is empty"},
		"two targets":     {base(RestoreMember{Kind: KindBucket, Name: "b", As: "c", InPlace: true}), "sets 2 targets"},
		"no target":       {base(RestoreMember{Kind: KindBucket, Name: "b"}), "sets 0 targets"},
		"catalog as":      {base(RestoreMember{Kind: KindCatalogService, Name: "lake", As: "x"}), "takes asCatalog or inPlace"},
		"store asCatalog": {base(RestoreMember{Kind: KindKVStore, Name: "s", AsCatalog: &CatalogRef{Bucket: "b", Prefix: "p"}}), "takes as or inPlace"},
		"other kind":      {base(RestoreMember{Kind: KindFunction, Name: "f", InPlace: true}), "want KVStore, Bucket or CatalogService"},
		"repeated member": {base(RestoreMember{Kind: KindKVStore, Name: "s", InPlace: true}, RestoreMember{Kind: KindKVStore, Name: "s", As: "t"}), "repeats"},
		"no identity": {func() *Restore {
			r := base(RestoreMember{Kind: KindKVStore, Name: "s", InPlace: true})
			r.Spec.Source.IdentitySecret = ""
			return r
		}(), "identitySecret"},
	} {
		t.Run(name, func(t *testing.T) {
			err := c.r.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate = %v, want Invalid naming %q", err, c.want)
			}
		})
	}
}

// An App's backupSchedules entry with an empty scope becomes a BackupSchedule part scoped to the App; a set scope is
// kept; a ref entry is no part.
func TestAppBackupSchedulePart(t *testing.T) {
	spec := ordersNightly(nil).Spec
	scoped := spec
	spec.Scope = BackupScope{}
	a := &App{TypeMeta: typeMetaFor(KindApp), ObjectMeta: ObjectMeta{Name: "orders", Namespace: "orders", ResourceGroup: "rg"},
		Spec: AppSpec{BackupSchedules: []AppBackupSchedule{{Name: "nightly", BackupScheduleSpec: spec},
			{Name: "named", BackupScheduleSpec: scoped}, {Ref: "shared"}}}}
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	parts := a.Parts()
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if got := parts[0].(*BackupSchedule).Spec.Scope; got.App != "orders" {
		t.Fatalf("empty scope = %+v, want app orders", got)
	}
	if got := parts[1].(*BackupSchedule).Spec.Scope; len(got.KVStores) != 1 || got.App != "" {
		t.Fatalf("set scope = %+v, kept", got)
	}
	if a.Spec.BackupSchedules[0].Scope.App != "" {
		t.Fatal("the App's spec stays as declared")
	}
	if refs := a.Refs(); len(refs) != 1 || refs[0].Kind != KindBackupSchedule {
		t.Fatalf("refs = %v", refs)
	}
	b, err := json.Marshal(a.Spec.BackupSchedules[2])
	if err != nil || string(b) != `{"ref":"shared"}` {
		t.Fatalf("a ref entry marshals as its ref alone: %s, %v", b, err)
	}
}
