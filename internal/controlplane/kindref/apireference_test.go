package kindref

import (
	"encoding/json"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
)

// guideSections gives each section of the guide that holds a feature a title and an icon, as specgen reads them.
func guideSections() map[string]SectionMeta {
	out := map[string]SectionMeta{}
	for _, f := range Features {
		s := topFolder(f.Path)
		out[s] = SectionMeta{Title: "The " + s + " section", Icon: "Folder"}
	}
	return out
}

func metaOf(t *testing.T, files map[string][]byte, name string) meta {
	t.Helper()
	require.Contains(t, files, name)
	var m meta
	require.NoError(t, json.Unmarshal(files[name], &m))
	return m
}

// inOrder reports whether got holds distinct entries of want, in want's order.
func inOrder(got, want []string) bool {
	last := -1
	for _, g := range got {
		i := slices.Index(want, g)
		if i <= last {
			return false
		}
		last = i
	}
	return true
}

// checkLayout holds the reference's files to paths and to the order of ADR-0331 Decision 3.2: the sections in the
// order of Features, a section's kinds in that order and then its tags by name, a kind's operations list, create,
// get, replace and delete and then by name, no folder without a page, and no other file.
func checkLayout(t *testing.T, files map[string][]byte, paths map[string]string) {
	t.Helper()
	var written map[string]string
	require.NoError(t, json.Unmarshal(files[pathsFile], &written))
	require.Equal(t, paths, written, "reference-paths.json")

	var sections []string
	tags := map[string][]string{}
	for _, f := range Features {
		s := topFolder(f.Path)
		if !slices.Contains(sections, s) {
			sections = append(sections, s)
		}
		for _, k := range f.Kinds {
			tags[s] = append(tags[s], strings.ToLower(string(k)))
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(TagFeatures)) {
		s := topFolder(TagFeatures[tag])
		tags[s] = append(tags[s], strings.ToLower(tag))
	}
	rank := func(name string) int {
		if i := slices.Index(verbs, name); i >= 0 {
			return i
		}
		return len(verbs)
	}

	seen := map[string]bool{pathsFile: true, referenceDir + "/meta.json": true}
	root := metaOf(t, files, referenceDir+"/meta.json")
	require.Equal(t, meta{Root: true, Title: "API reference", Description: "The operations of funcd's control plane API",
		Icon: "SquareCode", Pages: root.Pages}, root)
	require.Equal(t, "index", root.Pages[0], "the Overview first")
	require.True(t, inOrder(root.Pages[1:], sections), "the sections in the order of Features: %v", root.Pages)
	var shown []string
	for _, s := range root.Pages[1:] {
		name := referenceDir + "/" + s + "/meta.json"
		seen[name] = true
		section := metaOf(t, files, name)
		require.Equal(t, meta{Title: guideSections()[s].Title, Icon: guideSections()[s].Icon, Pages: section.Pages}, section)
		require.NotEmpty(t, section.Pages, s)
		require.True(t, inOrder(section.Pages, tags[s]), "%s: its kinds in the order of Features, then its tags by name: %v",
			s, section.Pages)
		for _, k := range section.Pages {
			name := referenceDir + "/" + s + "/" + k + "/meta.json"
			seen[name] = true
			kind := metaOf(t, files, name)
			require.Equal(t, k, strings.ToLower(kind.Title), "a kind's folder is titled with the kind")
			require.NotEmpty(t, kind.Pages, k)
			for i, page := range kind.Pages {
				shown = append(shown, s+"/"+k+"/"+page)
				if i > 0 {
					prev := kind.Pages[i-1]
					require.True(t, rank(prev) < rank(page) || rank(prev) == rank(page) && prev < page,
						"%s/%s: %s before %s", s, k, prev, page)
				}
			}
		}
	}
	require.ElementsMatch(t, slices.Collect(maps.Values(paths)), shown, "the folders list each page once")
	for name := range files {
		require.True(t, seen[name], "%s is no folder of the reference", name)
	}
}

func TestReferencePaths(t *testing.T) {
	t.Run("the API's operations", func(t *testing.T) {
		doc := controlplane.SpecAPI().OpenAPI()
		paths := ReferencePaths(doc)
		ops := operationsOf(doc)
		var covered []string
		for _, o := range ops {
			if o.described() && !slices.Contains(Omitted, v1.Kind(o.tag())) {
				covered = append(covered, o.op.OperationID)
			}
		}
		require.NotEmpty(t, covered)
		require.ElementsMatch(t, covered, slices.Collect(maps.Keys(paths)), "one path per covered operation")
		owner := map[string]string{}
		for _, o := range ops {
			id := o.op.OperationID
			p, ok := paths[id]
			if !ok {
				continue
			}
			require.NotContains(t, owner, p, "%s and %s share a page", owner[p], id)
			owner[p] = id
			parts := strings.Split(p, "/")
			require.Len(t, parts, 3, p)
			feature := slices.IndexFunc(Features, func(f Feature) bool { return slices.Contains(featureOperations(ops, f), o) })
			require.GreaterOrEqual(t, feature, 0, "%s: a Spec tab lists it", id)
			require.Equal(t, topFolder(Features[feature].Path), parts[0], "%s: the section of its Spec tab", id)
			require.Equal(t, strings.ToLower(o.tag()), parts[1], id)
			for _, verb := range verbs {
				if id == verb+o.tag() {
					require.Equal(t, verb, parts[2], "%s: its kind's name is stripped", id)
				}
			}
			if plural, ok := strings.CutPrefix(id, "list"); ok && strings.EqualFold(plural, path.Base(o.path)) {
				require.Equal(t, "list", parts[2], "%s: its kind's plural is stripped", id)
			}
		}
		require.Equal(t, "services/kvstore/list", paths["listKVStores"])
		require.Equal(t, "workloads/function/get-logs", paths["getFunctionLogs"])

		files, err := ReferenceFiles(doc, guideSections())
		require.NoError(t, err)
		checkLayout(t, files, paths)
	})

	t.Run("the layout's rules", func(t *testing.T) {
		undescribed := &huma.Operation{OperationID: "listBuckets", Tags: []string{"Bucket"},
			Responses: map[string]*huma.Response{"200": {Description: "OK"}}}
		doc := referenceDoc(describedOp("listPolicies", "Policy"), describedOp("getPolicy", "Policy"),
			describedOp("returnQuarantined", "Hold"), describedOp("releaseHold", "Hold"),
			describedOp("getPlatformBackup", "PlatformBackup"), describedOp("listRestores", "Restore"),
			describedOp("deleteBackup", "Backup"), describedOp("listBackups", "Backup"), describedOp("listServices", "Service"),
			undescribed)
		paths := ReferencePaths(doc)
		require.Equal(t, map[string]string{
			"listPolicies":      "security-and-networking/policy/list",
			"getPolicy":         "security-and-networking/policy/get",
			"returnQuarantined": "disaster-recovery/hold/return-quarantined",
			"releaseHold":       "disaster-recovery/hold/release",
			"getPlatformBackup": "disaster-recovery/platformbackup/get",
			"listRestores":      "disaster-recovery/restore/list",
			"deleteBackup":      "disaster-recovery/backup/delete",
			"listBackups":       "disaster-recovery/backup/list",
		}, paths, "an Omitted kind's operation and an undescribed one have no page")
		files, err := ReferenceFiles(doc, guideSections())
		require.NoError(t, err)
		checkLayout(t, files, paths)
		require.Equal(t, []string{"index", "security-and-networking", "disaster-recovery"},
			metaOf(t, files, referenceDir+"/meta.json").Pages, "a section with no page has no folder")
		require.Equal(t, []string{"backup", "restore", "hold", "platformbackup"},
			metaOf(t, files, referenceDir+"/disaster-recovery/meta.json").Pages)
		require.Equal(t, []string{"list", "delete"}, metaOf(t, files, referenceDir+"/disaster-recovery/backup/meta.json").Pages)
		require.Equal(t, []string{"release", "return-quarantined"},
			metaOf(t, files, referenceDir+"/disaster-recovery/hold/meta.json").Pages)

		_, err = ReferenceFiles(doc, map[string]SectionMeta{})
		require.ErrorContains(t, err, "the guide has no section security-and-networking")

		lease := referenceDoc(describedOp("getLease", "Lease"))
		require.Empty(t, ReferencePaths(lease))
		_, err = ReferenceFiles(lease, guideSections())
		require.ErrorContains(t, err, "no section of the API reference holds getLease")

		_, err = ReferenceFiles(referenceDoc(describedOp("getFunctionLogs", "Function"),
			describedOp("getLogsFunction", "Function")), guideSections())
		require.ErrorContains(t, err, "would share the reference page workloads/function/get-logs")
	})
}

// referenceDoc is a document of ops, each at a path of its own.
func referenceDoc(ops ...*huma.Operation) *huma.OpenAPI {
	paths := map[string]*huma.PathItem{}
	for _, op := range ops {
		paths["/"+op.OperationID] = &huma.PathItem{Get: op}
	}
	return &huma.OpenAPI{Paths: paths}
}

// describedOp is a described operation tagged tag: it has a summary and a refusal.
func describedOp(id, tag string) *huma.Operation {
	return &huma.Operation{OperationID: id, Summary: id, Tags: []string{tag},
		Responses: map[string]*huma.Response{"200": {Description: "OK"}, "404": {Description: "No such object."}}}
}

// scenario: spec-tab-links-the-reference
func TestScenarioSpecTabLinksTheReference(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	pages, err := Render(doc)
	require.NoError(t, err)
	files, err := ReferenceFiles(doc, guideSections())
	require.NoError(t, err)
	var paths map[string]string
	require.NoError(t, json.Unmarshal(files[pathsFile], &paths))

	kv := section(t, string(pages["develop/services/kv/spec.md"]), "## HTTP API")
	rows := 0
	for _, o := range operationsOf(doc) {
		if o.tag() != string(v1.KindKVStore) || !o.described() {
			continue
		}
		rows++
		require.Contains(t, kv, "\n| `"+o.method+"` | `"+o.path+"` | ["+o.op.Summary+"](../../../api/"+
			paths[o.op.OperationID]+".md) |", "%s: its method and path, and its summary linking its page", o.op.OperationID)
	}
	require.Positive(t, rows)
	require.Equal(t, rows, strings.Count(kv, "\n| `"), "one row per KVStore operation")
	require.Contains(t, kv, "| [Replace a KVStore](../../../api/services/kvstore/replace.md) |")

	inReference := map[string]bool{}
	for _, p := range paths {
		inReference["api/"+p+".md"] = true
	}
	link := regexp.MustCompile(`\]\(([^)]+)\)`)
	d := documentOf(doc)
	for _, f := range Features {
		name := "develop/" + f.Path + "/spec.md"
		page := string(pages[name])
		ops := featureOperations(d.ops, f)
		if len(ops) == 0 {
			require.NotContains(t, page, "\n## HTTP API\n", "%s: no operation, no section", name)
			continue
		}
		links := link.FindAllStringSubmatch(section(t, page, "## HTTP API"), -1)
		require.Len(t, links, len(ops), "%s: a link for each operation", name)
		for _, l := range links {
			require.True(t, inReference[path.Join(path.Dir(name), l[1])], "%s links %s, which reference-paths.json does not "+
				"hold", name, l[1])
		}
	}
}

// Undescribed holds the fields of the API reference's schemas that no Spec page shows (ADR-0331 Decision 3.2): a
// body's, an answer's and the problem's that a refusal returns, but not a kind's own, which its Spec page shows.
func TestUndescribedCoversTheReferenceSchemas(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	fields, err := fieldsOf(documentOf(doc))
	require.NoError(t, err)
	for _, field := range []string{"FaultError.type", "FaultError.detail", "KVStoreHandover.workflow",
		"ContractViolation.details", "Detail.message", "LogsOutputBody.items"} {
		require.Contains(t, fields, field)
	}
	require.NotContains(t, fields, "KVStore.apiVersion", "a kind's Spec page shows its apiVersion")
	require.NotContains(t, Undescribed(doc), "FaultError.detail")
	doc.Components.Schemas.Map()["FaultError"].Properties["detail"].Description = ""
	require.Contains(t, Undescribed(doc), "FaultError.detail")
}
