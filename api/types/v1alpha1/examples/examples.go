// Package examples holds the guide's example manifests (ADR-0325): one scenario per file under <feature>/, applied by
// pkg/funcd's TestScenarioGuideExamplesApply. <feature> is the last part of the feature's path in kindref's table. A
// file named after a kind (<feature>/<kind>.yaml, in lower case) marks that kind's example in a region named after
// the kind, which its feature's Spec page includes.
package examples

import "embed"

// FS holds every example file.
//
//go:embed */*.yaml
var FS embed.FS
