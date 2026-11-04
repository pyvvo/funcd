// Command behavior writes the generated value blocks of docs/BEHAVIOR.md (ADR-0313), each chapter's config keys and
// hard-coded values, and with -check fails when the file no longer matches the code: a value changed, a number is written
// inline, a package, config section or kind has no chapter, or a written row cites what does not exist.
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
	docPath          = "docs/BEHAVIOR.md"
	blockBegin       = "<!-- generated values, do not edit: just behavior-values -->"
	blockEnd         = "<!-- end of generated values -->"
	rowHeader        = "| Where | What happens | Time | You see | Status | Source |"
	notWritten       = "Not written yet."
	maxWritten       = 800
	maxRow           = 400
	firstADRWithRows = 314
	skillHint        = "run the /behavior-registry skill: regenerate with `just behavior-values`, then check that each written row still holds"
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
		{"Functions", []string{"internal/function", "internal/pooling", "internal/artifact", "internal/contract", "internal/contractcheck", "internal/revhold"}, []string{"functions"}},
		{"Calls", []string{"internal/edge", "internal/gateway", "internal/dataplane", "internal/activator", "internal/invoke", "internal/route", "internal/site"}, []string{"server", "invoke", "site"}},
		{"Services", []string{"internal/kvstore", "internal/blob", "internal/sql", "internal/lake", "internal/services", "internal/provider"}, []string{"kvstore", "blob", "s3gateway", "lake", "sql"}},
		{"Eventing", []string{"internal/bus", "internal/eventing", "internal/sensor", "internal/workflow", "internal/expr"}, []string{"workflow", "eventing"}},
		{"Apps", []string{"internal/app"}, []string{"app"}},
		{"Disaster recovery", []string{"internal/backup", "internal/restore", "internal/services/backup", "internal/platform/hold", "internal/snapshot"}, []string{"backup", "workloadBackup"}},
		{"Health", []string{"internal/health"}, []string{"health"}},
		{"Limits", []string{"internal/quota"}, nil},
		{"funcdctl dev", []string{"cmd/funcdctl", "pkg/sdk"}, nil},
	}
}

// inputs is what a run reads besides the tree at root, so a test brings its own.
type inputs struct {
	root     string
	doc      string
	chapters []chapter
	keys     map[string]string
	kinds    []string
}

// skipped holds no platform behavior: tests, tools and benches.
var skipped = regexp.MustCompile(`^(\.[a-z]+|vendor|node_modules|e2e|examples|scripts|docs|images|bench|tests|api/cron|api/lintfixture|internal/testkit|internal/controlplane/cmd)(/|$)`)

// nameTest is a name that reads as a limit, a wait or a count, so its plain number counts as a value.
var nameTest = regexp.MustCompile(`(?i)^(default|max|min|limit|grace|timeout|interval|retry|retries|attempts|budget)|(max|limit|timeout|interval|grace|wait|period|after|budget|attempts|retries|bytes|size|ttl|page|batch|concurrency|conns|buf)$`)

// numberText is a number held in a string, such as a default read later as a duration or a size.
var numberText = regexp.MustCompile(`^\d+(\.\d+)?(ms|s|m|h|Ki|Mi|Gi|KiB|MiB|GiB)?$`)

// row is one generated value.
type row struct{ name, value, from string }

func main() {
	check := flag.Bool("check", false, "fail when the file differs from what the code gives, or a written row does not hold")
	doc := flag.String("doc", docPath, "the file to read or write, to try a chapter on a copy")
	flag.Parse()
	in, err := realInputs()
	if err == nil {
		in.doc = *doc
		err = run(in, *check)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func realInputs() (inputs, error) {
	documented, err := exampleDefaults("examples/funcdconfig.yaml")
	if err != nil {
		return inputs{}, err
	}
	keys := map[string]string{}
	leaves(reflect.ValueOf(config.Defaults()), "", func(key string, v reflect.Value) {
		section, _, _ := strings.Cut(key, ".")
		if section == "apiVersion" || section == "kind" {
			return
		}
		value := fmt.Sprint(v.Interface())
		if v.IsZero() {
			value = cmp.Or(documented[key], "(empty)")
		}
		keys[key] = value
	})
	var kinds []string
	for _, k := range v1.AllKinds() {
		kinds = append(kinds, string(k))
	}
	return inputs{root: ".", chapters: chapters(), keys: keys, kinds: kinds}, nil
}

func run(in inputs, check bool) error {
	code, err := scanCode(in)
	if err != nil {
		return err
	}
	byChapter, problems := configRows(in)
	problems = append(problems, code.orphans...)
	for name, rows := range code.rows {
		byChapter[name] = append(byChapter[name], rows...)
	}
	path := cmp.Or(in.doc, docPath)
	if !filepath.IsAbs(path) {
		path = filepath.Join(in.root, path)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, missing := render(string(current), in.chapters, byChapter)
	problems = append(problems, missing...)
	if !check {
		if len(problems) > 0 {
			return failure(problems)
		}
		return os.WriteFile(path, []byte(next), 0o600)
	}
	problems = append(problems, code.inline...)
	problems = append(problems, validate(in, string(current), byChapter, code.known)...)
	problems = append(problems, adrsWithoutRows(in.root)...)
	if changed := changedLines(string(current), next); len(changed) > 0 {
		problems = append(problems, fmt.Sprintf("%s no longer matches the code:\n  %s", docPath, strings.Join(changed, "\n  ")))
	}
	if len(problems) > 0 {
		return failure(problems)
	}
	return nil
}

func failure(problems []string) error {
	return fmt.Errorf("%s\n\n%s", strings.Join(problems, "\n"), skillHint)
}

func home(chapters []chapter, dir string) (string, bool) {
	best, name := -1, ""
	for _, c := range chapters {
		for _, p := range c.packages {
			if (dir == p || strings.HasPrefix(dir, p+"/")) && len(p) > best {
				best, name = len(p), c.name
			}
		}
	}
	return name, best >= 0
}

// scanned is what the production code gives: its values by chapter, every string and identifier it holds, the numbers
// it writes inline and the packages with no chapter.
type scanned struct {
	rows    map[string][]row
	known   map[string]bool
	inline  []string
	orphans []string
}

func scanCode(in inputs) (scanned, error) {
	out := scanned{rows: map[string][]row{}, known: map[string]bool{}}
	fset := token.NewFileSet()
	err := filepath.WalkDir(in.root, func(full string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path, err := filepath.Rel(in.root, full)
		if err != nil {
			return err
		}
		path = filepath.ToSlash(path)
		if d.IsDir() {
			if path != "." && skipped.MatchString(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "/bench.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, full, nil, parser.ParseComments)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		if ast.IsGenerated(f) || importsTesting(f) {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				if s, uerr := strconv.Unquote(v.Value); uerr == nil && v.Kind == token.STRING {
					out.known[s] = true
				}
			case *ast.Ident:
				out.known[v.Name] = true
			}
			return true
		})
		name, ok := home(in.chapters, filepath.ToSlash(filepath.Dir(path)))
		if !ok {
			if p := "package " + filepath.ToSlash(filepath.Dir(path)) + " has no chapter in scripts/behavior/main.go"; !slices.Contains(out.orphans, p) {
				out.orphans = append(out.orphans, p)
			}
			return nil
		}
		rows, inline := fileValues(fset, f, path, in.keys)
		out.rows[name] = append(out.rows[name], rows...)
		out.inline = append(out.inline, inline...)
		return nil
	})
	slices.Sort(out.orphans)
	return out, err
}

func importsTesting(f *ast.File) bool {
	return slices.ContainsFunc(f.Imports, func(im *ast.ImportSpec) bool { return im.Path.Value == `"testing"` })
}

// fileValues returns f's package-level values, its fallbacks that differ from their key, and its inline numbers.
func fileValues(fset *token.FileSet, f *ast.File, path string, keys map[string]string) (rows []row, inline []string) {
	scan := func(name string, body ast.Node) {
		r, i := inlineNumbers(fset, body, path, name, keys)
		rows, inline = append(rows, r...), append(inline, i...)
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				scan(d.Name.Name, d.Body)
			}
		case *ast.GenDecl:
			if d.Tok != token.CONST && d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, v := range vs.Values {
					if i >= len(vs.Names) {
						break
					}
					if value, ok := valueOf(fset, v, vs.Names[i].Name); ok {
						rows = append(rows, row{vs.Names[i].Name, value, path})
					}
					ast.Inspect(v, func(n ast.Node) bool {
						if lit, ok := n.(*ast.FuncLit); ok {
							scan(vs.Names[i].Name, lit.Body)
							return false
						}
						return true
					})
				}
			}
		}
	}
	return rows, inline
}

// inlineNumbers finds the numbers a function body writes inline. One beside its config key is that key's hard-coded
// fallback: it has no row while it equals the key's default, and any other is refused.
func inlineNumbers(fset *token.FileSet, body ast.Node, path, fn string, keys map[string]string) (rows []row, inline []string) {
	var stack []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if refused(n, stack, fset) {
			e, _ := n.(ast.Expr)
			if key := besideKey(stack, keys); key != "" {
				if v, ok := eval(e); ok && differs(keys[key], v, hasUnit(e)) {
					rows = append(rows, row{key + " (fallback)", formatValue(v, hasUnit(e)), path})
				}
			} else {
				inline = append(inline, fmt.Sprintf("%s: %s() writes the number %s inline: give it a name at package level", path, fn, exprText(fset, n)))
			}
			return false
		}
		stack = append(stack, n)
		return true
	})
	return rows, inline
}

// refused says whether n is a literal duration or size, or a bare unit used as a value. A bare unit stays allowed in
// arithmetic, in a conversion, and as the unit given to rounding.
func refused(n ast.Node, stack []ast.Node, fset *token.FileSet) bool {
	switch v := n.(type) {
	case *ast.BinaryExpr:
		switch v.Op {
		case token.MUL:
			return (isUnit(v.X) && isNumber(v.Y)) || (isUnit(v.Y) && isNumber(v.X)) || isKibi(v)
		case token.SHL:
			return isNumber(v.X) && isNumber(v.Y)
		}
	case *ast.SelectorExpr:
		if !isUnit(v) || !slices.Contains([]string{"Second", "Minute", "Hour"}, v.Sel.Name) || len(stack) == 0 {
			return false
		}
		i := len(stack) - 1
		for i > 0 {
			if _, paren := stack[i].(*ast.ParenExpr); !paren {
				break
			}
			i--
		}
		switch p := stack[i].(type) {
		case *ast.CallExpr:
			callee := exprText(fset, p.Fun)
			return !isConversion(callee) && !slices.Contains([]string{"ceilUnits", "ceilDiv"}, callee) &&
				!strings.HasSuffix(callee, ".Round") && !strings.HasSuffix(callee, ".Truncate")
		case *ast.KeyValueExpr, *ast.AssignStmt, *ast.ReturnStmt, *ast.ValueSpec:
			return true
		case *ast.BinaryExpr:
			return slices.Contains([]token.Token{token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ}, p.Op)
		}
	}
	return false
}

func isConversion(callee string) bool {
	return slices.Contains([]string{"time.Duration", "v1.Duration", "Duration", "int", "int32", "int64", "uint", "uint32", "uint64", "float32", "float64"}, callee)
}

// isKibi is a literal `N * 1024`: a size written without a shift.
func isKibi(b *ast.BinaryExpr) bool {
	if !isNumber(b.X) || !isNumber(b.Y) {
		return false
	}
	x, y := exprNumber(b.X), exprNumber(b.Y)
	return x == 1024 || y == 1024
}

func exprNumber(e ast.Expr) int64 {
	n, _ := eval(e)
	return n
}

// besideKey returns the config key a string literal names in the same call or composite literal.
func besideKey(stack []ast.Node, keys map[string]string) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if _, isStmt := stack[i].(ast.Stmt); isStmt {
			return ""
		}
		var args []ast.Expr
		switch s := stack[i].(type) {
		case *ast.CallExpr:
			args = s.Args
		case *ast.CompositeLit:
			args = s.Elts
		}
		for _, a := range args {
			if kv, ok := a.(*ast.KeyValueExpr); ok {
				a = kv.Value
			}
			if l, ok := a.(*ast.BasicLit); ok && l.Kind == token.STRING {
				if s, err := strconv.Unquote(l.Value); err == nil {
					if _, known := keys[s]; known {
						return s
					}
				}
			}
		}
	}
	return ""
}

func differs(have string, n int64, duration bool) bool {
	have, _, _ = strings.Cut(have, ",") // a derived default reads "30s, or three supervisionPeriod when larger"
	if duration {
		d, err := time.ParseDuration(have)
		return err != nil || int64(d) != n
	}
	return have != strconv.FormatInt(n, 10) && have != formatSize(n)
}

func formatValue(n int64, duration bool) string {
	if duration {
		return formatDuration(time.Duration(n))
	}
	return formatSize(n)
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

// isNumber is a literal, or arithmetic or a conversion over literals only.
func isNumber(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.INT || v.Kind == token.FLOAT
	case *ast.ParenExpr:
		return isNumber(v.X)
	case *ast.UnaryExpr:
		return isNumber(v.X)
	case *ast.BinaryExpr:
		return isNumber(v.X) && isNumber(v.Y)
	case *ast.CallExpr:
		if len(v.Args) != 1 {
			return false
		}
		_, ident := v.Fun.(*ast.Ident)
		s, sel := v.Fun.(*ast.SelectorExpr)
		return (ident || sel && s.Sel.Name == "Duration") && isNumber(v.Args[0])
	}
	return false
}

func unit(name string) (time.Duration, bool) {
	d, ok := map[string]time.Duration{
		"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond, "Millisecond": time.Millisecond,
		"Second": time.Second, "Minute": time.Minute, "Hour": time.Hour,
	}[name]
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
	case *ast.UnaryExpr:
		n, ok := eval(v.X)
		if v.Op == token.SUB {
			n = -n
		}
		return n, ok && (v.Op == token.SUB || v.Op == token.ADD)
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

func hasSize(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if b, ok := n.(*ast.BinaryExpr); ok && ((b.Op == token.SHL && isNumber(b.X) && isNumber(b.Y)) || (b.Op == token.MUL && isKibi(b))) {
			found = true
		}
		return true
	})
	return found
}

// plain is an expression of only numbers, names, units, shifts and conversions: a string, a composite literal, a function
// or any other call is no value.
func plain(e ast.Expr, fset *token.FileSet) bool {
	ok := true
	ast.Inspect(e, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			ok = ok && (v.Kind == token.INT || v.Kind == token.FLOAT)
		case *ast.CompositeLit, *ast.FuncLit:
			ok = false
		case *ast.CallExpr:
			ok = ok && isConversion(exprText(fset, v.Fun))
		}
		return ok
	})
	return ok
}

// valueOf returns a package-level value's text when it is a duration, a size, or a count or a derived value by its name.
func valueOf(fset *token.FileSet, e ast.Expr, name string) (string, bool) {
	if l, ok := e.(*ast.BasicLit); ok && l.Kind == token.STRING {
		s, err := strconv.Unquote(l.Value)
		return s, err == nil && numberText.MatchString(s) && nameTest.MatchString(name)
	}
	if !plain(e, fset) {
		return "", false
	}
	n, ok := eval(e)
	switch {
	case hasUnit(e) && ok:
		return formatDuration(time.Duration(n)), true
	case hasSize(e) && ok:
		return formatSize(n), true
	case hasUnit(e) || hasSize(e):
		return "`" + exprText(fset, e) + "`", true
	case ok && nameTest.MatchString(name):
		return strconv.FormatInt(n, 10), true
	case nameTest.MatchString(name):
		return "`" + exprText(fset, e) + "`", true
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

// configRows puts every config key with its default into its section's chapter.
func configRows(in inputs) (map[string][]row, []string) {
	sections := map[string]string{}
	for _, c := range in.chapters {
		for _, s := range c.sections {
			sections[s] = c.name
		}
	}
	byChapter := map[string][]row{}
	var orphans []string
	for key, value := range in.keys {
		section, _, _ := strings.Cut(key, ".")
		name, ok := sections[section]
		if !ok {
			if p := "config section " + section + " has no chapter in scripts/behavior/main.go"; !slices.Contains(orphans, p) {
				orphans = append(orphans, p)
			}
			continue
		}
		byChapter[name] = append(byChapter[name], row{key, value, "config"})
	}
	slices.Sort(orphans)
	return byChapter, orphans
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

// exampleDefaults reads the default the example config documents for each key, for the keys whose default is derived at
// start and so is empty in the config's defaults.
func exampleDefaults(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
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
func render(text string, chapters []chapter, byChapter map[string][]row) (string, []string) {
	var problems []string
	for _, c := range chapters {
		heading := "\n## " + c.name + "\n"
		start := strings.Index(text, heading)
		if start < 0 {
			problems = append(problems, fmt.Sprintf("%s has no chapter %q", docPath, c.name))
			continue
		}
		bodyStart := start + len(heading)
		end := len(text)
		if i := strings.Index(text[bodyStart:], "\n## "); i >= 0 {
			end = bodyStart + i + 1
		}
		body := text[bodyStart:end]
		i, j := strings.Index(body, blockBegin), strings.Index(body, blockEnd)
		switch {
		case i >= 0 && j > i:
			body = body[:i] + block(c.name, byChapter[c.name]) + body[j+len(blockEnd):]
		case i >= 0:
			problems = append(problems, fmt.Sprintf("%s: chapter %q has a generated block with no end", docPath, c.name))
		default:
			body = strings.TrimRight(body, "\n") + "\n\n" + block(c.name, byChapter[c.name]) + "\n"
		}
		text = text[:bodyStart] + body + text[end:]
	}
	return text, problems
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
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", strings.ReplaceAll(r.name, "|", `\|`), cmp.Or(strings.ReplaceAll(r.value, "|", `\|`), "(empty)"), from)
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
	issueRef  = regexp.MustCompile(`#\d+`)
	tick      = regexp.MustCompile("`([^`]+)`")
	reasonish = regexp.MustCompile(`\b[A-Z][A-Za-z]*[a-z][A-Z][A-Za-z]+\b`)
	cited     = regexp.MustCompile("`([A-Za-z][A-Za-z0-9.]*)` (\\d+(?:\\.\\d+)?) ?(ms|s|min|h|KiB|MiB|GiB)?(?:[^\\w.:]|[.:](?:\\s|$)|$)")
	link      = regexp.MustCompile(`\]\(([^)#\s]+)(#[^)]*)?\)`)
	record    = regexp.MustCompile(`#\d+|card "[^"]+"`)
	dated     = regexp.MustCompile(`decider \d{4}-\d{2}-\d{2}`)
	holder    = regexp.MustCompile(`#\d+|card "[^"]+"|ADR-\d{4} Open questions`)
	adrStatus = regexp.MustCompile(`(?m)^- \*\*Status\*\*: (.*)$`)
)

func cells(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "|"), "|")
	var out []string
	var b strings.Builder
	for i := range len(line) {
		if line[i] == '|' && (i == 0 || line[i-1] != '\\') {
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteByte(line[i])
	}
	return append(out, strings.TrimSpace(b.String()))
}

// rowProblem checks a row of a chapter's table: six cells, and a status that cites what its word promises.
func rowProblem(line string) string {
	c := cells(line)
	if len(c) != 6 {
		return fmt.Sprintf("a row has %d cells, not 6", len(c))
	}
	if len(line) > maxRow {
		return fmt.Sprintf("a row has %d characters, over the cap of %d: cut it or split it", len(line), maxRow)
	}
	status, source := strings.Trim(c[4], "*"), c[5]
	switch status {
	case "decided":
		if !adrRef.MatchString(source) && (!dated.MatchString(source) || !record.MatchString(source)) {
			return "a row that says decided cites no ADR, and no dated decider with its record"
		}
	case "accepted":
		if !adrRef.MatchString(source) && !testRef.MatchString(source) && !issueRef.MatchString(source) && (!dated.MatchString(source) || !record.MatchString(source)) {
			return "a row that says accepted cites no ADR, no test, no closed fix, and no dated decider with its record"
		}
	case "deferred":
		if !holder.MatchString(source) {
			return `a row that says deferred cites no card "<title>", no ADR-NNNN Open questions and no issue`
		}
	case "uncovered":
		if source == "" {
			return "a row that says uncovered does not say what was read"
		}
	default:
		return "status " + status + " is not decided, accepted, deferred or uncovered"
	}
	return ""
}

var (
	machineEdge  = regexp.MustCompile(`^\s*(\S+)\s+-->\s+(\S+)`)
	machineState = regexp.MustCompile(`^\s*state\s+(\S+)`)
	newWords     = regexp.MustCompile(`New words?: ([^.]*)\.`)
)

// section is what a chapter's written part holds, in order.
type section struct {
	states         []string
	marked         map[string]bool
	name           string
	line           int
	kinds, machine int
	table          int
	notWritten     bool
}

func (s section) problems() []string {
	if s.name == "" {
		return nil
	}
	at := fmt.Sprintf("%s:%d: chapter %s", docPath, s.line, s.name)
	var out []string
	switch {
	case s.kinds == 0:
		out = append(out, at+" has no Kinds: line")
	case s.notWritten:
		out = append(out, at+" reads "+notWritten)
		return out
	case s.machine == 0:
		out = append(out, at+" has no machine")
	case s.table == 0:
		out = append(out, at+" has no table")
	case s.kinds > s.machine || s.machine > s.table:
		out = append(out, at+" does not follow the order: Kinds: line, machine, note, table")
	}
	return out
}

// unmarked names a machine's states that the code does not have and the note does not mark new.
func (s section) unmarked(known map[string]bool) []string {
	var out []string
	for _, state := range s.states {
		if !known[state] && !s.marked[state] {
			out = append(out, fmt.Sprintf("%s:%d: chapter %s: the machine's state %s is not in the code and the note does not mark it new", docPath, s.line, s.name, state))
		}
	}
	return out
}

// validate checks the written part: each chapter's shape, each row's status, and every ADR, test, reason, link and number
// it cites.
func validate(in inputs, text string, byChapter map[string][]row, known map[string]bool) []string {
	var problems []string
	names := map[string]bool{}
	for _, c := range in.chapters {
		names[c.name] = true
	}
	values := map[string][]string{}
	for _, rows := range byChapter {
		for _, r := range rows {
			values[r.name] = append(values[r.name], r.value)
		}
	}
	tests := testNames(in.root)
	var current section
	kindLines := ""
	written := 0
	inBlock, inFence, inTable := false, false, false
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
			if !inFence && line == "```mermaid" && current.machine == 0 {
				current.machine = n + 1
			}
			inFence = !inFence
			continue
		}
		if inFence {
			for _, m := range [][]string{machineEdge.FindStringSubmatch(line), machineState.FindStringSubmatch(line)} {
				for _, state := range m[min(1, len(m)):] {
					if state != "[*]" && !slices.Contains(current.states, state) {
						current.states = append(current.states, state)
					}
				}
			}
			continue
		}
		at := fmt.Sprintf("%s:%d", docPath, n+1)
		if name, ok := strings.CutPrefix(line, "## "); ok {
			problems = append(problems, current.problems()...)
			problems = append(problems, current.unmarked(known)...)
			current = section{name: name, line: n + 1, marked: map[string]bool{}}
			if !names[name] {
				problems = append(problems, at+": chapter "+name+" is not in scripts/behavior/main.go")
			}
		}
		if strings.HasPrefix(line, "Kinds:") {
			kindLines += line + "\n"
			current.kinds = n + 1
		}
		if line == notWritten {
			current.notWritten = true
		}
		if m := newWords.FindStringSubmatch(line); m != nil && current.marked != nil {
			for _, word := range strings.Split(m[1], ",") {
				current.marked[strings.TrimSpace(word)] = true
			}
		}
		switch {
		case line == rowHeader:
			inTable = true
			if current.table == 0 {
				current.table = n + 1
			}
		case !strings.HasPrefix(line, "|"):
			inTable = false
		case inTable && !strings.HasPrefix(line, "|---"):
			if p := rowProblem(line); p != "" {
				problems = append(problems, at+": "+p)
			}
		}
		for _, m := range adrRef.FindAllStringSubmatch(line, -1) {
			if p := adrProblem(in.root, m[1]); p != "" {
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
				if !strings.HasPrefix(word, "Test") && !strings.Contains(m[1], "/") && !known[word] && values[word] == nil {
					problems = append(problems, at+": no status reason, value or name in the code called "+word)
				}
			}
		}
		for _, m := range link.FindAllStringSubmatch(line, -1) {
			if !strings.Contains(m[1], "://") {
				if _, err := os.Stat(filepath.Join(in.root, filepath.Dir(docPath), m[1])); err != nil {
					problems = append(problems, at+": links to "+m[1]+", which does not exist")
				}
			}
		}
		for _, m := range cited.FindAllStringSubmatch(line, -1) {
			have, ok := values[m[1]]
			switch {
			case !ok && known[m[1]]: // a name in the code that is no value, such as a fault kind beside a status code
			case !ok:
				problems = append(problems, fmt.Sprintf("%s: cites %s %s %s: no config key or value has that name", at, m[1], m[2], m[3]))
			case !slices.ContainsFunc(have, func(v string) bool { return sameValue(v, m[2], m[3]) }):
				problems = append(problems, fmt.Sprintf("%s: %s is %s in the code, not %s %s", at, m[1], strings.Join(have, ", "), m[2], m[3]))
			}
		}
	}
	problems = append(problems, current.problems()...)
	problems = append(problems, current.unmarked(known)...)
	if written > maxWritten {
		problems = append(problems, fmt.Sprintf("%s: the written part is %d lines, over the cap of %d", docPath, written, maxWritten))
	}
	for _, k := range in.kinds {
		if !regexp.MustCompile(`\b` + k + `\b`).MatchString(kindLines) {
			problems = append(problems, fmt.Sprintf("%s: kind %s is on no chapter's Kinds line", docPath, k))
		}
	}
	return problems
}

func sameValue(have, number, unit string) bool {
	have, _, _ = strings.Cut(have, ",") // a derived default reads "30s, or three supervisionPeriod when larger"
	unit = strings.Replace(unit, "min", "m", 1)
	if d, err := time.ParseDuration(number + unit); err == nil && (unit != "" || number == "0") {
		h, herr := time.ParseDuration(have)
		if herr == nil {
			return h == d
		}
	}
	return strings.EqualFold(have, number+unit) || strings.EqualFold(have+"B", number+unit)
}

func adrProblem(root, number string) string {
	files, _ := filepath.Glob(filepath.Join(root, "docs/adr", number+"-*.md"))
	if len(files) != 1 {
		return "does not exist"
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return err.Error()
	}
	head, _, _ := strings.Cut(string(data), "\n## ")
	status := ""
	if m := adrStatus.FindStringSubmatch(head); m != nil {
		status = m[1]
	}
	switch {
	case strings.HasPrefix(status, "Superseded") || strings.Contains(head, "- **Superseded by**"):
		return "is superseded: cite the ADR that replaced it"
	case !strings.HasPrefix(status, "Accepted") && !strings.HasPrefix(status, "Reviewing") && !strings.HasPrefix(status, "Implemented"):
		return "is not accepted: " + cmp.Or(status, "it has no Status line")
	}
	return ""
}

// adrsWithoutRows names each ADR past ADR-0313 with no Behavior rows section.
func adrsWithoutRows(root string) []string {
	files, _ := filepath.Glob(filepath.Join(root, "docs/adr", "[0-9][0-9][0-9][0-9]-*.md"))
	var out []string
	for _, f := range files {
		n, err := strconv.Atoi(filepath.Base(f)[:4])
		if err != nil || n < firstADRWithRows {
			continue
		}
		data, err := os.ReadFile(f)
		if err == nil && !strings.Contains(string(data), "\n## Behavior rows\n") {
			out = append(out, "docs/adr/"+filepath.Base(f)+" has no Behavior rows section")
		}
	}
	return out
}

func testNames(root string) map[string]bool {
	names := map[string]bool{}
	test := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != root {
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
