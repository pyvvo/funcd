package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// logsCmd prints a function's logs from the control-plane logs route (ADR-0084), tenant-scoped to the
// caller's identity. Default renders a subset (time · severity · replica · body); -o wide appends the
// structured attributes inline as key=value; -o json emits one JSON record per line.
func (a *cli) logsCmd() *cobra.Command {
	var ns, since, severity, output string
	var limit int
	cmd := &cobra.Command{
		Use:   "logs <function>",
		Short: "Print a function's logs (tenant-scoped to your identity)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput("funcdctl logs", output, "wide", "json"); err != nil {
				return err
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			lines, err := c.Logs(cmd.Context(), v1.NamespaceName(ns), v1.ObjectName(args[0]), sdk.LogsOptions{
				Since: since, Severity: severity, Limit: limit,
			})
			if err != nil {
				return err
			}
			return a.renderLogLines(lines, output)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace")
	cmd.Flags().StringVar(&since, "since", "", "only logs since (RFC3339 time or a duration like 15m)")
	cmd.Flags().StringVar(&severity, "severity", "", "minimum level: trace|debug|info|warn|error|fatal")
	cmd.Flags().IntVar(&limit, "limit", 0, "max records to return, most-recent (default 1000)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: wide (append source/inv/attrs inline) | json")
	return cmd
}

// renderLogLines writes log lines in the funcdctl format (ADR-0084), shared by `funcdctl logs` and
// `funcdctl workflow logs` (ADR-0106): "" ⇒ time·severity·replica·body, wide ⇒ + source/inv/attrs inline,
// json ⇒ one JSON record per line.
func (a *cli) renderLogLines(lines []logread.Line, output string) error {
	for _, l := range lines {
		if output == "json" {
			b, merr := json.Marshal(l)
			if merr != nil {
				return fault.Internalf("funcdctl logs", "marshal record: %v", merr)
			}
			if werr := a.writef("%s\n", string(b)); werr != nil {
				return werr
			}
			continue
		}
		line := l.Time.UTC().Format(time.RFC3339) + " [" + l.Severity + "] " + l.Replica + " " + l.Body
		if output == "wide" {
			line += wideSuffix(l.Source, l.Invocation, l.TraceID, l.Attrs)
		}
		if werr := a.writef("%s\n", line); werr != nil {
			return werr
		}
	}
	return nil
}

// wideSuffix renders source/inv/trace + the structured attrs as inline " key=value" pairs (attrs sorted),
// so -o wide gives the full record as readable text instead of JSON.
func wideSuffix(source, inv, traceID string, attrs json.RawMessage) string {
	var b strings.Builder
	if source != "" {
		b.WriteString(" source=" + source)
	}
	if inv != "" {
		b.WriteString(" inv=" + inv)
	}
	if traceID != "" {
		b.WriteString(" trace=" + traceID)
	}
	if len(attrs) > 0 {
		var m map[string]json.RawMessage
		if json.Unmarshal(attrs, &m) == nil {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := string(m[k])
				if s, err := strconv.Unquote(v); err == nil { // a JSON string value → its text
					v = s
				}
				b.WriteString(" " + k + "=" + v)
			}
		}
	}
	return b.String()
}
