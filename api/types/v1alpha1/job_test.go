package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

func job(edit func(*JobSpec)) *Job {
	j := &Job{TypeMeta: typeMetaFor(KindJob), ObjectMeta: ObjectMeta{Name: "hello", Namespace: "default", ResourceGroup: "rg1"},
		Spec: JobSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci-layout://layout/hello:1.0.0"}}
	if edit != nil {
		edit(&j.Spec)
	}
	return j
}

// ADR-0347 Decision 1: a Job's spec holds a Function's fields with their checks, then its own timeout and concurrency.
func TestJobValidate(t *testing.T) {
	t.Parallel()
	local := VolumeEntry{Name: "state", Type: VolumeLocal, Size: "1GiB"}
	for name, c := range map[string]struct {
		edit func(*JobSpec)
		want string // "" is valid
	}{
		"minimal":              {nil, ""},
		"empty spec":           {func(s *JobSpec) { *s = JobSpec{} }, ""},
		"timeout past an hour": {func(s *JobSpec) { s.Timeout = Duration(2 * time.Hour) }, ""},
		"timeout 168h":         {func(s *JobSpec) { s.Timeout = MaxJobTimeout }, ""},
		"timeout past 168h":    {func(s *JobSpec) { s.Timeout = MaxJobTimeout + Duration(time.Hour) }, "spec.timeout 169h is out of bounds"},
		"concurrency 15":       {func(s *JobSpec) { s.Concurrency = MaxJobConcurrency }, ""},
		"concurrency 16":       {func(s *JobSpec) { s.Concurrency = 16 }, "spec.concurrency 16 is outside 1 to 15"},
		"concurrency negative": {func(s *JobSpec) { s.Concurrency = -1 }, "spec.concurrency -1 is outside 1 to 15"},
		"local volume, one run": {func(s *JobSpec) {
			s.Volumes, s.Concurrency = []VolumeEntry{local}, 1
		}, ""},
		"local volume, two runs": {func(s *JobSpec) {
			s.Volumes, s.Concurrency = []VolumeEntry{{Name: "tmp", Type: VolumeEmptyDir, Size: "1GiB"}, local}, 2
		}, "spec.concurrency 2 with spec.volumes[1], a local volume: a local volume allows at most 1 run at once"},
		"emptyDir volume, two runs": {func(s *JobSpec) {
			s.Volumes, s.Concurrency = []VolumeEntry{{Name: "tmp", Type: VolumeEmptyDir, Size: "1GiB"}}, 2
		}, ""},
		"volume check": {func(s *JobSpec) {
			s.Volumes = []VolumeEntry{{Name: "tmp", Type: VolumeEmptyDir}}
		}, "spec.volumes[0] sets neither size nor sizeBytes"},
		"binding check": {func(s *JobSpec) {
			s.Bindings.KV = []FunctionKV{{Alias: "t", Store: "s", Table: "a"}, {Alias: "t", Store: "s", Table: "b"}}
		}, `spec.bindings.kv alias "t" is duplicated`},
		"link timeout check": {func(s *JobSpec) {
			s.Bindings.Links = []FunctionLink{{Alias: "api", Target: "api", Timeout: Duration(6 * time.Minute)}}
		}, "spec.bindings.links[0].timeout 6m is out of bounds"},
		"resources check": {func(s *JobSpec) { s.Resources.CPU = "0.001" }, "spec.resources.cpu"},
		"root entry check": {func(s *JobSpec) {
			s.KVStores = []KVStoreEntry{{Name: "s"}, {Name: "s"}}
		}, `repeats the name "s"`},
		"secret declaration check": {func(s *JobSpec) {
			s.SecretDeclarations = []SecretDeclaration{{Name: "creds"}}
		}, "spec.secrets[0].keys is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := job(c.edit).Validate()
			if c.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), c.want)
		})
	}
}

// ADR-0347 Decision 3: a run names its Job; its input and its fixed fields are admission's.
func TestJobRunValidate(t *testing.T) {
	t.Parallel()
	run := func(job ObjectName) *JobRun {
		return &JobRun{TypeMeta: typeMetaFor(KindJobRun),
			ObjectMeta: ObjectMeta{Name: "hello-1a2b3c4d", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       JobRunSpec{Job: job, Input: []byte(`{"name":"Ada"}`)}}
	}
	require.NoError(t, run("hello").Validate())
	for _, bad := range []ObjectName{"", "Hello", "-hello"} {
		err := run(bad).Validate()
		require.Equal(t, fault.Invalid, fault.KindOf(err), "spec.job %q", bad)
		require.ErrorContains(t, err, "spec.job")
	}
	wrongKind := run("hello")
	wrongKind.Kind = KindWorkflowRun
	require.ErrorContains(t, wrongKind.Validate(), "does not match")
}

// ADR-0347 Decision 2: exactly one of spec.function and spec.job names a Revision's subject.
func TestRevisionNamesOneSubject(t *testing.T) {
	t.Parallel()
	fn := ObjectRef{Kind: KindFunction, Namespace: "default", Name: "hello"}
	for name, c := range map[string]struct {
		spec RevisionSpec
		want string
	}{
		"a Function's": {RevisionSpec{Function: fn, Number: 1}, ""},
		"a Job's":      {RevisionSpec{Job: "hello", Number: 1}, ""},
		"neither":      {RevisionSpec{Number: 1}, "spec sets neither function nor job: want exactly one of function and job"},
		"both":         {RevisionSpec{Function: fn, Job: "hello", Number: 1}, "spec sets both function and job"},
		"bad job":      {RevisionSpec{Job: "Hello", Number: 1}, `spec.job "Hello" is not a valid DNS-1123 label`},
	} {
		rev := &Revision{TypeMeta: typeMetaFor(KindRevision),
			ObjectMeta: ObjectMeta{Name: "hello-v1", Namespace: "default", ResourceGroup: "rg1"}, Spec: c.spec}
		err := rev.Validate()
		if c.want == "" {
			require.NoError(t, err, name)
			continue
		}
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.ErrorContains(t, err, c.want, name)
	}
}

func revisionHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:8]
}

// ADR-0347 Decision 2: a Job's Revision is <job>-v<generation>, cut and hashed past 63 characters with -v<generation>
// last, as ADR-0172 Decision 4 shortens a Function's and ADR-0339 an Addon's.
func TestJobRevisionName(t *testing.T) {
	t.Parallel()
	n60, n63 := strings.Repeat("a", 60), strings.Repeat("c", 63)
	for _, c := range []struct {
		job  string
		gen  int64
		want string
	}{
		{"hello", 1, "hello-v1"},
		{"hello", 2, "hello-v2"},
		{n60, 9, n60 + "-v9"},
		{n60, 10, n60[:50] + "-" + revisionHash(n60) + "-v10"},
		{n63, 1, n63[:51] + "-" + revisionHash(n63) + "-v1"},
		{n63, math.MaxInt64, n63[:33] + "-" + revisionHash(n63) + "-v9223372036854775807"},
	} {
		got := JobRevisionName(ObjectName(c.job), c.gen)
		require.Equal(t, ObjectName(c.want), got, "%d characters at generation %d", len(c.job), c.gen)
		require.NoError(t, got.Validate())
		require.Equal(t, got, JobRevisionName(ObjectName(c.job), c.gen), "stable")
	}
	require.Equal(t, ObjectName(n63[:51]+"-"+revisionHash(n63)+"-10"), AddonRevisionName(ObjectName(n63), 10),
		"an Addon's revision keeps its <addon>-<n> shortening")
}

// ADR-0347 Decision 2: a Function's Revision name ends in -<digits> (ADR-0172 Decision 4), and a Job's never does, so no
// Job's Revision name is ever a Function's.
func TestJobRevisionNameIsNeverAFunctionRevisionName(t *testing.T) {
	t.Parallel()
	functionRevision := regexp.MustCompile(`-[0-9]+$`)
	for n := 1; n <= 63; n++ {
		for _, name := range []string{strings.Repeat("x", n), strings.Repeat("9", n), "a" + strings.Repeat("-1", (n-1)/2)} {
			if len(name) > 63 || !dnsLabel.MatchString(name) {
				continue
			}
			for _, gen := range []int64{1, 9, 10, 99, 123456, math.MaxInt64} {
				got := string(JobRevisionName(ObjectName(name), gen))
				require.LessOrEqual(t, len(got), 63)
				require.Truef(t, strings.HasSuffix(got, "-v"+strconv.FormatInt(gen, 10)), "%q ends in -v<generation>", got)
				require.Falsef(t, functionRevision.MatchString(got), "%q has a Function Revision name's shape", got)
			}
		}
	}
}
