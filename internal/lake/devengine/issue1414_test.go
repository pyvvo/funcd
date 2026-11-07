//go:build dev

package devengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Lake reconciler hands an engine its key pair through ProviderSpec.IssueEnv (ADR-0270 Decision 5), and the dev
// engine's S3 secret is made from that pair (ADR-0130 Decision 3).
func TestIssue1414_DevEngineCreatesItsS3SecretFromTheIssuedKeyPair(t *testing.T) {
	r := fakeRuntime(t)
	spec := testSpec()
	spec.Env["AWS_ENDPOINT_URL_S3"] = "http://127.0.0.1:3006"
	spec.Env["AWS_ACCESS_KEY_ID"] = "AKSTALE"
	spec.IssueEnv = func() map[string]string {
		return map[string]string{"AWS_ACCESS_KEY_ID": "AKISSUED", "AWS_SECRET_ACCESS_KEY": "SKISSUED"}
	}
	if st, err := r.Converge(t.Context(), spec); err != nil || !st.Ready {
		t.Fatalf("Converge = %+v, %v; want Ready", st, err)
	}
	sql, err := os.ReadFile(filepath.Join(r.procs[spec.Ref].dir, "init.sql"))
	if err != nil {
		t.Fatalf("read the engine's init SQL: %v", err)
	}
	const want = "CREATE OR REPLACE SECRET funcd_dev_s3 (TYPE s3, KEY_ID 'AKISSUED', SECRET 'SKISSUED', REGION 'us-east-1', " +
		"ENDPOINT '127.0.0.1:3006', URL_STYLE 'path', USE_SSL false);"
	if !strings.Contains(string(sql), want) {
		t.Errorf("the init SQL lacks %q:\n%s", want, sql)
	}
	if got := spec.Env["AWS_ACCESS_KEY_ID"]; got != "AKSTALE" {
		t.Errorf("Converge changed the spec's Env: AWS_ACCESS_KEY_ID = %q", got)
	}
}
