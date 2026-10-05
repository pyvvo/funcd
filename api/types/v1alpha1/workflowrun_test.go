package v1alpha1

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// workflowRunSrc is embedded rather than read from disk so a `go test -overlay` revert check sees the
// overlaid source.
//
//go:embed workflowrun.go
var workflowRunSrc string

func workflowRunSpecFieldDoc(t *testing.T, field string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "workflowrun.go", workflowRunSrc, parser.ParseComments)
	require.NoError(t, err)
	var doc string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "WorkflowRunSpec" {
			return true
		}
		for _, fl := range ts.Type.(*ast.StructType).Fields.List {
			if len(fl.Names) == 1 && fl.Names[0].Name == field {
				doc = strings.Join(strings.Fields(fl.Doc.Text()), " ")
			}
		}
		return false
	})
	require.NotEmpty(t, doc, "WorkflowRunSpec.%s has no doc comment", field)
	return doc
}

func TestIssue514_PausedDocSaysTerminalRunIgnoresIt(t *testing.T) {
	const rule = "Ignored once the run is already terminal"
	require.Contains(t, workflowRunSpecFieldDoc(t, "Cancel"), rule)
	doc := workflowRunSpecFieldDoc(t, "Paused")
	require.Contains(t, doc, rule, "Paused doc must state the Cancel rule for a terminal run (#419)")
	require.Contains(t, doc, "keeps its phase")
}

// scenario: child-name-never-admitted
func TestScenarioChildNameNeverAdmitted(t *testing.T) {
	run := func(name string, replay *ReplaySeed) *WorkflowRun {
		return &WorkflowRun{
			TypeMeta:   TypeMeta{APIVersion: KindWorkflowRun.GVK().APIVersion(), Kind: KindWorkflowRun},
			ObjectMeta: ObjectMeta{Name: ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
			Spec:       WorkflowRunSpec{Workflow: "wf", Replay: replay},
		}
	}
	require.NoError(t, run("p-sub", &ReplaySeed{Run: "p-sub", From: "y"}).Validate())
	for field, r := range map[string]*WorkflowRun{
		"metadata.name":   run("p.sub", nil),
		"spec.replay.run": run("rep", &ReplaySeed{Run: "p.sub", From: "y"}),
	} {
		require.Equal(t, fault.Invalid, fault.KindOf(r.Validate()), "a WorkflowRun whose %s is p.sub", field)
	}
}
