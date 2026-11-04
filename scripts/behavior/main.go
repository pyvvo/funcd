// Command behavior writes the generated value blocks of docs/BEHAVIOR.md, each chapter's config keys and hard-coded
// values, and with -check fails when the file no longer matches the code: a value changed, a package has no chapter, or
// a written row cites an ADR, a test, a reason or a number that is not so.
package main

import (
	"cmp"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
)

const (
	docPath    = "docs/BEHAVIOR.md"
	blockBegin = "<!-- generated values, do not edit: just behavior-values -->"
	blockEnd   = "<!-- end of generated values -->"
	maxWritten = 800
	skillHint  = "run the /behavior-registry skill: regenerate with `just behavior-values`, then check that each written row still holds"
)

// chapter is one chapter of the file: the packages and the config sections its values come from.
type chapter struct {
	name     string
	packages []string
	sections []string
}

func chapters() []chapter {
	return []chapter{
		{"Platform lifecycle", []string{"pkg/funcd", "cmd/funcd", "cmd/funcd-rqlite", "internal/lifecycle", "internal/safemode", "internal/upgrade", "internal/platform"}, []string{"storage", "recovery", "log", "telemetry"}},
		{"Controller", []string{"internal/controller", "internal/gc", "internal/store", "internal/ownedstore"}, []string{"controller"}},
		{"Admission", []string{"internal/controlplane", "api/types", "api/openapi", "api/fault"}, nil},
		{"Security", []string{"internal/auth", "internal/secrets", "internal/envresolve", "internal/network"}, []string{"auth", "secrets"}},
		{"Workers", []string{"internal/runtime", "internal/workernode", "internal/volume", "internal/funclog", "internal/scheduler"}, []string{"runtime", "volumes", "funclog"}},
		{"Functions", []string{"internal/function", "internal/pooling", "internal/artifact", "internal/contract", "internal/contractcheck", "internal/revhold", "internal/snapshot"}, []string{"functions"}},
		{"Calls", []string{"internal/edge", "internal/gateway", "internal/dataplane", "internal/activator", "internal/invoke", "internal/route", "internal/site"}, []string{"server", "invoke", "site"}},
		{"Services", []string{"internal/kvstore", "internal/blob", "internal/sql", "internal/lake", "internal/services", "internal/provider"}, []string{"kvstore", "blob", "s3gateway", "lake", "sql"}},
		{"Eventing", []string{"internal/bus", "internal/eventing", "internal/sensor", "internal/workflow", "internal/expr"}, []string{"workflow", "eventing"}},
		{"Apps", []string{"internal/app"}, []string{"app"}},
		{"Disaster recovery", []string{"internal/backup", "internal/restore", "internal/services/backup", "internal/platform/hold"}, []string{"backup", "workloadBackup"}},
		{"Health", []string{"internal/health"}, []string{"health"}},
		{"Limits", []string{"internal/quota"}, nil},
		{"funcdctl dev", []string{"cmd/funcdctl", "pkg/sdk"}, nil},
	}
}

// skipped holds no platform behavior: tests, tools and benches.
var skipped = regexp.MustCompile(`^(\.[a-z]+|vendor|node_modules|e2e|examples|scripts|docs|images|bench|tests|api/cron|api/lintfixture|internal/testkit|internal/controlplane/cmd)(/|$)`)

// row is one generated value.
type row struct{ name, value, from string }

// limitName is a name that reads as a limit, a wait or a count, so its plain number counts as a value.
var limitName = regexp.MustCompile(`(?i)^(default|max|min|limit|grace|timeout|interval|retry|retries|attempts|budget)|(max|limit|timeout|interval|grace|wait|period|budget|attempts|retries|bytes|size|ttl)$`)

func main() {
	check := flag.Bool("check", false, "fail when the file differs from what the code gives, or a written row does not hold")
	flag.Parse()
	if err := run(*check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(check bool) error {
	byChapter := map[string][]row{}
	stringsInCode := map[string]bool{}
	keys, err := configRows(byChapter)
	if err != nil {
		return err
	}
	if err := scanCode(byChapter, stringsInCode, keys); err != nil {
		return err
	}
	current, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	next, err := render(string(current), byChapter)
	if err != nil {
		return err
	}
	if !check {
		return os.WriteFile(docPath, []byte(next), 0o600)
	}
	problems := validate(next, byChapter, keys, stringsInCode)
	if changed := changedLines(string(current), next); len(changed) > 0 {
		problems = append(problems, fmt.Sprintf("%s no longer matches the code:\n  %s", docPath, strings.Join(changed, "\n  ")))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s\n\n%s", strings.Join(problems, "\n"), skillHint)
	}
	return nil
}

func home(dir string) (string, bool) {
	best, name := -1, ""
	for _, c := range chapters() {
		for _, p := range c.packages {
			if (dir == p || strings.HasPrefix(dir, p+"/")) && len(p) > best {
				best, name = len(p), c.name
			}
		}
	}
	return name, best >= 0
}

func scanCode(byChapter map[string][]row, literals map[string]bool, keys map[string]string) error {
	fset := token.NewFileSet()
	var orphans []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && skipped.MatchString(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "/bench.go") || strings.Contains(path, "zz_generated") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if l, ok := n.(*ast.BasicLit); ok && l.Kind == token.STRING {
				if s, uerr := strconv.Unquote(l.Value); uerr == nil {
					literals[s] = true
				}
			}
			return true
		})
		chapterName, ok := home(filepath.Dir(path))
		if !ok {
			if d := filepath.Dir(path); !slices.Contains(orphans, d) {
				orphans = append(orphans, d)
			}
			return nil
		}
		byChapter[chapterName] = append(byChapter[chapterName], fileRows(fset, f, path, keys)...)
		return nil
	})
	if err != nil {
		return err
	}
	if len(orphans) > 0 {
		slices.Sort(orphans)
		return fmt.Errorf("these packages have no chapter in scripts/behavior/main.go: %s\n\n%s", strings.Join(orphans, ", "), skillHint)
	}
	return nil
}

// fileRows returns f's named values and the numbers written inline in its functions.
func fileRows(fset *token.FileSet, f *ast.File, path string, keys map[string]string) []row {
	var out []row
	named := map[ast.Node]bool{}
	for _, decl := range f.Decls {
		prefix := ""
		if fd, ok := decl.(*ast.FuncDecl); ok {
			prefix = fd.Name.Name + "(): "
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				v := vs.Values[i]
				ast.Inspect(v, func(m ast.Node) bool { named[m] = true; return true })
				if value, ok := valueOf(fset, v, name.Name); ok {
					out = append(out, row{prefix + name.Name, value, path})
				}
			}
			return true
		})
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var stack []ast.Node
		ast.Inspect(fd, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			b, ok := n.(*ast.BinaryExpr)
			if !ok || named[n] || b.Op != token.MUL || !isUnit(b.X) && !isUnit(b.Y) || !isNumber(b.X) && !isNumber(b.Y) {
				stack = append(stack, n)
				return true
			}
			if d, ok := eval(b); ok {
				if name, ok := inlineName(fset, stack, fd.Name.Name, time.Duration(d), keys); ok {
					out = append(out, row{name, formatDuration(time.Duration(d)), path})
				}
			}
			return false
		})
	}
	return out
}

// inlineName says what an inline number sets, from the code around it. A number that is a config key's hard-coded
// fallback has no row of its own while it equals the key's default: only a fallback that differs is worth reading.
func inlineName(fset *token.FileSet, stack []ast.Node, fn string, d time.Duration, keys map[string]string) (string, bool) {
	for i := len(stack) - 1; i >= 0; i-- {
		if _, isStmt := stack[i].(ast.Stmt); isStmt {
			break
		}
		var args []ast.Expr
		switch s := stack[i].(type) {
		case *ast.CallExpr:
			args = s.Args
		case *ast.CompositeLit:
			args = s.Elts
		}
		if key := keyIn(args, keys); key != "" {
			have, _, _ := strings.Cut(keys[key], ",") // a derived default reads "30s, or three supervisionPeriod when larger"
			if h, err := time.ParseDuration(have); err == nil && h == d {
				return "", false
			}
			return key + " (fallback)", true
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		switch p := stack[i].(type) {
		case *ast.ParenExpr:
			continue
		case *ast.CallExpr:
			callee := exprText(fset, p.Fun)
			if slices.Contains([]string{"time.Duration", "v1.Duration", "int64", "float64", "max", "min"}, callee) {
				continue
			}
			return fn + "(): " + callee, true
		case *ast.KeyValueExpr:
			return fn + "(): " + exprText(fset, p.Key), true
		case *ast.AssignStmt:
			return fn + "(): " + exprText(fset, p.Lhs[0]), true
		case *ast.ReturnStmt:
			return fn + "(): return", true
		case *ast.CompositeLit:
			if p.Type == nil {
				continue
			}
			return fn + "(): " + exprText(fset, p.Type), true
		case *ast.BinaryExpr:
			if t := exprText(fset, p); len(t) <= 60 {
				return fn + "(): " + t, true
			}
		}
		break
	}
	return fn + "()", true
}

func keyIn(args []ast.Expr, keys map[string]string) string {
	for _, a := range args {
		if l, ok := a.(*ast.BasicLit); ok && l.Kind == token.STRING {
			if s, err := strconv.Unquote(l.Value); err == nil {
				if _, known := keys[s]; known {
					return s
				}
			}
		}
	}
	return ""
}

func exprText(fset *token.FileSet, n ast.Node) string {
	var b strings.Builder
	_ = printer.Fprint(&b, fset, n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func isUnit(e ast.Expr) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := s.X.(*ast.Ident)
	_, known := unit(s.Sel.Name)
	return ok && x.Name == "time" && known
}

func isNumber(e ast.Expr) bool {
	l, ok := e.(*ast.BasicLit)
	return ok && (l.Kind == token.INT || l.Kind == token.FLOAT)
}

func unit(name string) (time.Duration, bool) {
	d, ok := map[string]time.Duration{"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond, "Millisecond": time.Millisecond,
		"Second": time.Second, "Minute": time.Minute, "Hour": time.Hour}[name]
	return d, ok
}

// eval computes a constant expression of numbers and time units.
func eval(e ast.Expr) (int64, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		n, err := strconv.ParseInt(strings.ReplaceAll(v.Value, "_", ""), 0, 64)
		return n, err == nil
	case *ast.ParenExpr:
		return eval(v.X)
	case *ast.SelectorExpr:
		if isUnit(v) {
			d, _ := unit(v.Sel.Name)
			return int64(d), true
		}
	case *ast.CallExpr: // a conversion: time.Duration(n), int64(n)
		if len(v.Args) == 1 {
			return eval(v.Args[0])
		}
	case *ast.BinaryExpr:
		x, ok1 := eval(v.X)
		y, ok2 := eval(v.Y)
		if !ok1 || !ok2 {
			return 0, false
		}
		switch v.Op {
		case token.MUL:
			return x * y, true
		case token.ADD:
			return x + y, true
		case token.SUB:
			return x - y, true
		case token.SHL:
			return x << y, true
		case token.QUO:
			if y != 0 {
				return x / y, true
			}
		}
	}
	return 0, false
}

func hasUnit(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if s, ok := n.(ast.Expr); ok && isUnit(s) {
			found = true
		}
		return true
	})
	return found
}

func hasShift(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.SHL {
			found = true
		}
		return true
	})
	return found
}

// valueOf returns a named value's text when it is a duration, a size or a limit by name.
func valueOf(fset *token.FileSet, e ast.Expr, name string) (string, bool) {
	n, ok := eval(e)
	switch {
	case hasUnit(e):
		if ok {
			return formatDuration(time.Duration(n)), true
		}
		var b strings.Builder
		_ = printer.Fprint(&b, fset, e)
		return "`" + b.String() + "`", true
	case hasShift(e) && ok:
		return formatSize(n), true
	case ok && limitName.MatchString(name):
		return strconv.FormatInt(n, 10), true
	}
	return "", false
}

func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func formatSize(n int64) string {
	for _, u := range []struct {
		shift uint
		name  string
	}{{40, "TiB"}, {30, "GiB"}, {20, "MiB"}, {10, "KiB"}} {
		if n >= 1<<u.shift && n%(1<<u.shift) == 0 {
			return fmt.Sprintf("%d%s", n>>u.shift, u.name)
		}
	}
	return strconv.FormatInt(n, 10)
}

// configRows adds every config key with its default to its section's chapter and returns the keys.
func configRows(byChapter map[string][]row) (map[string]string, error) {
	documented, err := exampleDefaults()
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	leaves(reflect.ValueOf(config.Defaults()), "", func(key string, v reflect.Value) {
		value := fmt.Sprint(v.Interface())
		if v.IsZero() {
			value = cmp.Or(documented[key], "(empty)")
		}
		keys[key] = value
	})
	sections := map[string]string{}
	for _, c := range chapters() {
		for _, s := range c.sections {
			sections[s] = c.name
		}
	}
	var orphans []string
	for key, value := range keys {
		section, _, _ := strings.Cut(key, ".")
		if section == "apiVersion" || section == "kind" {
			delete(keys, key)
			continue
		}
		name, ok := sections[section]
		if !ok {
			if !slices.Contains(orphans, section) {
				orphans = append(orphans, section)
			}
			continue
		}
		byChapter[name] = append(byChapter[name], row{key, value, "config"})
	}
	if len(orphans) > 0 {
		slices.Sort(orphans)
		return nil, fmt.Errorf("these config sections have no chapter in scripts/behavior/main.go: %s\n\n%s", strings.Join(orphans, ", "), skillHint)
	}
	return keys, nil
}

func leaves(v reflect.Value, prefix string, each func(string, reflect.Value)) {
	for i := range v.NumField() {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		if f := v.Field(i); f.Kind() == reflect.Struct {
			leaves(f, prefix+name+".", each)
		} else {
			each(prefix+name, f)
		}
	}
}

var (
	exampleKey     = regexp.MustCompile(`^( *)([A-Za-z0-9]+):(\s|$)`)
	exampleDefault = regexp.MustCompile(`default: (.*)$`)
)

// exampleDefaults reads the default examples/funcdconfig.yaml documents for each key, for the keys whose default is
// derived at start and so is empty in the config's defaults.
func exampleDefaults() (map[string]string, error) {
	data, err := os.ReadFile("examples/funcdconfig.yaml")
	if err != nil {
		return nil, err
	}
	type frame struct {
		indent int
		name   string
	}
	var stack []frame
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		text := line
		if i := len(line) - len(strings.TrimLeft(line, " ")); strings.HasPrefix(line[i:], "# ") {
			text = line[:i] + line[i+2:]
		}
		m := exampleKey.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= len(m[1]) {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, frame{len(m[1]), m[2]})
		names := make([]string, len(stack))
		for i, f := range stack {
			names[i] = f.name
		}
		value, _, _ := strings.Cut(text[len(m[0]):], "#") // the key's own value, when its line names no default
		if d := exampleDefault.FindStringSubmatch(line); d != nil {
			value, _, _ = strings.Cut(d[1], " (")
		}
		out[strings.Join(names, ".")] = strings.TrimSuffix(strings.TrimSpace(value), ".")
	}
	return out, nil
}

// render rewrites each chapter's generated block, at the end of the chapter when it has none yet.
func render(text string, byChapter map[string][]row) (string, error) {
	for _, c := range chapters() {
		heading := "\n## " + c.name + "\n"
		start := strings.Index(text, heading)
		if start < 0 {
			return "", fmt.Errorf("%s has no chapter %q\n\n%s", docPath, c.name, skillHint)
		}
		bodyStart := start + len(heading)
		end := len(text)
		if i := strings.Index(text[bodyStart:], "\n## "); i >= 0 {
			end = bodyStart + i + 1
		}
		body := text[bodyStart:end]
		if i := strings.Index(body, blockBegin); i >= 0 {
			j := strings.Index(body, blockEnd)
			if j < i {
				return "", fmt.Errorf("%s: chapter %q has a generated block with no end", docPath, c.name)
			}
			body = body[:i] + block(c.name, byChapter[c.name]) + body[j+len(blockEnd):]
		} else {
			body = strings.TrimRight(body, "\n") + "\n\n" + block(c.name, byChapter[c.name]) + "\n"
		}
		text = text[:bodyStart] + body + text[end:]
	}
	return text, nil
}

func block(name string, rows []row) string {
	slices.SortFunc(rows, func(a, b row) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a.from, b.from), cmp.Compare(a.name, b.name), cmp.Compare(a.value, b.value))
	})
	rows = slices.Compact(rows)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n<details>\n<summary>%s: values</summary>\n\n| Value | Default | From |\n|---|---|---|\n", blockBegin, name)
	for _, r := range rows {
		from := r.from
		if from != "config" {
			from = "`" + from + "`"
		}
		value := strings.ReplaceAll(r.value, "|", `\|`)
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", strings.ReplaceAll(r.name, "|", `\|`), cmp.Or(value, "(empty)"), from)
	}
	b.WriteString("\n</details>\n" + blockEnd)
	return b.String()
}

func rank(r row) int {
	if r.from == "config" {
		return 0
	}
	return 1
}

var (
	adrRef    = regexp.MustCompile(`ADR-(\d{4})`)
	testRef   = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	tick      = regexp.MustCompile("`([^`]+)`")
	reasonish = regexp.MustCompile(`\b[A-Z][A-Za-z]*[a-z][A-Z][A-Za-z]+\b`)
	cited     = regexp.MustCompile("`([A-Za-z][A-Za-z0-9.]*)` (\\d+(?:\\.\\d+)?) ?(ms|s|min|h|KiB|MiB|GiB)?\\b")
	decision  = regexp.MustCompile(`ADR-\d{4}|decider \d{4}-\d{2}-\d{2}`)
	holder    = regexp.MustCompile(`(?i)\bcard\b|#\d+`)
)

const rowHeader = "| Where | What happens | Time | You see | Status | Source |"

// rowProblem checks a row of a chapter's table: six cells, and a status that cites what its word promises.
func rowProblem(line string) string {
	cells := strings.Split(strings.Trim(line, "| "), " | ")
	if len(cells) != 6 {
		return fmt.Sprintf("a row has %d cells, not 6", len(cells))
	}
	status, source := strings.Trim(cells[4], "*"), cells[5]
	switch status {
	case "decided", "accepted":
		if !decision.MatchString(source) {
			return "a row that says " + status + " cites no ADR and no dated decider"
		}
	case "deferred":
		if !holder.MatchString(source) {
			return "a row that says deferred cites no card and no issue"
		}
	case "uncovered":
	default:
		return "status " + status + " is not decided, accepted, deferred or uncovered"
	}
	return ""
}

// validate checks the written part: every ADR, test and reason it cites exists, every value it cites with a number has
// that number, each row's status holds, and it stays short enough to read whole.
func validate(text string, byChapter map[string][]row, keys map[string]string, literals map[string]bool) []string {
	var problems []string
	known := map[string]bool{}
	for _, c := range chapters() {
		known[c.name] = true
	}
	kindLines := ""
	inTable := false
	values := map[string][]string{}
	for key, v := range keys {
		values[key] = append(values[key], v)
	}
	for _, rows := range byChapter {
		for _, r := range rows {
			values[r.name] = append(values[r.name], r.value)
		}
	}
	tests := testNames()
	written := 0
	inBlock, inFence := false, false
	for n, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, blockBegin):
			inBlock = true
			continue
		case strings.HasPrefix(line, blockEnd):
			inBlock = false
			continue
		case inBlock:
			continue
		}
		written++
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}
		if inFence {
			continue
		}
		at := fmt.Sprintf("%s:%d", docPath, n+1)
		if name, ok := strings.CutPrefix(line, "## "); ok && !known[name] {
			problems = append(problems, at+": chapter "+name+" is not in scripts/behavior/main.go")
		}
		if strings.HasPrefix(line, "Kinds:") {
			kindLines += line + "\n"
		}
		switch {
		case line == rowHeader:
			inTable = true
		case !strings.HasPrefix(line, "|"):
			inTable = false
		case inTable && !strings.HasPrefix(line, "|---"):
			if p := rowProblem(line); p != "" {
				problems = append(problems, at+": "+p)
			}
		}
		for _, m := range adrRef.FindAllStringSubmatch(line, -1) {
			if p := adrProblem(m[1]); p != "" {
				problems = append(problems, at+": ADR-"+m[1]+" "+p)
			}
		}
		for _, m := range testRef.FindAllStringSubmatch(line, -1) {
			if !tests[m[1]] {
				problems = append(problems, at+": no test named "+m[1])
			}
		}
		for _, m := range tick.FindAllStringSubmatch(line, -1) {
			for _, word := range reasonish.FindAllString(m[1], -1) {
				if !strings.HasPrefix(word, "Test") && !strings.Contains(m[1], "/") && !literals[word] && values[word] == nil {
					problems = append(problems, at+": no status reason, value or literal named "+word)
				}
			}
		}
		for _, m := range cited.FindAllStringSubmatch(line, -1) {
			have, ok := values[m[1]]
			if ok && !slices.ContainsFunc(have, func(v string) bool { return sameValue(v, m[2], m[3]) }) {
				problems = append(problems, fmt.Sprintf("%s: %s is %s in the code, not %s %s", at, m[1], strings.Join(have, ", "), m[2], m[3]))
			}
		}
	}
	if written > maxWritten {
		problems = append(problems, fmt.Sprintf("%s: the written part is %d lines, over the cap of %d", docPath, written, maxWritten))
	}
	for _, k := range v1.AllKinds() {
		if !regexp.MustCompile(`\b` + string(k) + `\b`).MatchString(kindLines) {
			problems = append(problems, fmt.Sprintf("%s: kind %s is on no chapter's Kinds line", docPath, k))
		}
	}
	return problems
}

func sameValue(have, number, unit string) bool {
	have, _, _ = strings.Cut(have, ",") // a derived default reads "30s, or three supervisionPeriod when larger"
	unit = strings.Replace(unit, "min", "m", 1)
	if d, err := time.ParseDuration(number + unit); err == nil && unit != "" {
		h, herr := time.ParseDuration(have)
		return herr == nil && h == d
	}
	return strings.EqualFold(have, number+unit)
}

func adrProblem(number string) string {
	files, _ := filepath.Glob("docs/adr/" + number + "-*.md")
	if len(files) != 1 {
		return "does not exist"
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return err.Error()
	}
	head, _, _ := strings.Cut(string(data), "## ")
	if regexp.MustCompile(`(?m)^- \*\*Status\*\*: Superseded`).MatchString(head) {
		return "is superseded: cite the ADR that replaced it"
	}
	return ""
}

func testNames() map[string]bool {
	names := map[string]bool{}
	test := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	_ = filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(path, ".") && path != "." {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, "_test.go") {
			data, rerr := os.ReadFile(path)
			if rerr == nil {
				for _, m := range test.FindAllSubmatch(data, -1) {
					names[string(m[1])] = true
				}
			}
		}
		return nil
	})
	return names
}

// changedLines names the rows that differ between the file and what the code gives.
func changedLines(current, next string) []string {
	if current == next {
		return nil
	}
	have := map[string]bool{}
	for _, l := range strings.Split(current, "\n") {
		have[l] = true
	}
	want := map[string]bool{}
	var out []string
	for _, l := range strings.Split(next, "\n") {
		want[l] = true
		if !have[l] {
			out = append(out, "now: "+l)
		}
	}
	for _, l := range strings.Split(current, "\n") {
		if !want[l] {
			out = append(out, "was: "+l)
		}
	}
	return out
}
