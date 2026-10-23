package v1alpha1

import (
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

// TestVolumesValidate covers ADR-0262 Decisions 1 and 6: the entry's shape and the one-replica rule.
func TestVolumesValidate(t *testing.T) {
	local := func(e VolumeEntry) VolumeEntry {
		e.Type, e.SizeBytes = VolumeLocal, 1<<20
		return e
	}
	cases := map[string]struct {
		spec FunctionSpec
		want string
	}{
		"local by name":   {FunctionSpec{Volumes: []VolumeEntry{local(VolumeEntry{Name: "state", Deletion: DeletionDelete})}}, ""},
		"local by ref":    {FunctionSpec{Volumes: []VolumeEntry{local(VolumeEntry{Ref: "state"})}}, ""},
		"emptyDir":        {FunctionSpec{Replicas: 3, Volumes: []VolumeEntry{{Name: "cache", Type: VolumeEmptyDir, SizeBytes: 1}}}, ""},
		"name and ref":    {FunctionSpec{Volumes: []VolumeEntry{local(VolumeEntry{Name: "a", Ref: "a"})}}, "both name and ref"},
		"neither":         {FunctionSpec{Volumes: []VolumeEntry{local(VolumeEntry{})}}, "neither name nor ref"},
		"bad name":        {FunctionSpec{Volumes: []VolumeEntry{local(VolumeEntry{Name: "Bad_Name"})}}, "DNS-1123"},
		"repeat":          {FunctionSpec{Volumes: []VolumeEntry{local(VolumeEntry{Name: "a"}), local(VolumeEntry{Ref: "a"})}}, "repeats volume"},
		"no size":         {FunctionSpec{Volumes: []VolumeEntry{{Name: "a", Type: VolumeLocal}}}, "neither size nor sizeBytes"},
		"bad type":        {FunctionSpec{Volumes: []VolumeEntry{{Name: "a", Type: "nfs", SizeBytes: 1}}}, "neither emptyDir nor local"},
		"emptyDir ref":    {FunctionSpec{Volumes: []VolumeEntry{{Ref: "a", Type: VolumeEmptyDir, SizeBytes: 1}}}, "declared by name"},
		"emptyDir delete": {FunctionSpec{Volumes: []VolumeEntry{{Name: "a", Type: VolumeEmptyDir, SizeBytes: 1, Deletion: DeletionDelete}}}, "goes with its worker"},
		"ref deletion":    {FunctionSpec{Volumes: []VolumeEntry{local(VolumeEntry{Ref: "a", Deletion: DeletionRetain})}}, "takes no deletion"},
		"bad deletion":    {FunctionSpec{Volumes: []VolumeEntry{local(VolumeEntry{Name: "a", Deletion: "keep"})}}, "neither retain nor delete"},
		"local replicas":  {FunctionSpec{Replicas: 2, Volumes: []VolumeEntry{local(VolumeEntry{Name: "state"})}}, "spec.replicas 2 with spec.volumes[0]"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.spec.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate = %v, want Invalid naming %q", err, c.want)
			}
		})
	}
}

// TestScenarioOneReplica: a local volume and replicas 2 are refused at apply, naming both fields (ADR-0262).
// scenario: one-replica
func TestScenarioOneReplica(t *testing.T) {
	s := FunctionSpec{Replicas: 2, Volumes: []VolumeEntry{{Name: "state", Type: VolumeLocal, SizeBytes: 1}}}
	err := s.Validate()
	if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "spec.replicas") || !strings.Contains(err.Error(), "spec.volumes[0]") {
		t.Fatalf("Validate = %v, want Invalid naming spec.replicas and spec.volumes[0]", err)
	}
}

// TestVolumeEnv: the env var is the name upper-cased, like FUNCD_LAKE_<ALIAS>_URL (ADR-0262 Decision 2).
func TestVolumeEnv(t *testing.T) {
	if got := VolumeEnv("model-cache"); got != "FUNCD_VOLUME_MODEL-CACHE" {
		t.Fatalf("VolumeEnv = %q", got)
	}
}

// TestBackupScopeVolumes: scope.volumes holds <function>/<name> pairs of DNS labels, without repeats (Decision 11).
func TestBackupScopeVolumes(t *testing.T) {
	for v, want := range map[string]string{"cart/state": "", "cart": "want <function>/<name>", "cart/Bad": "want <function>/<name>"} {
		err := BackupScope{Volumes: []string{v}}.validate("op")
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Fatalf("validate(%q) = %v, want %q", v, err, want)
		}
	}
	if err := (BackupScope{Volumes: []string{"cart/state", "cart/state"}}).validate("op"); err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("a repeat = %v, want repeats", err)
	}
}
