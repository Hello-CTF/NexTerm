package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const dbtestPackageSuffix = "/internal/dbtest"

type listedPackage struct {
	Dir          string
	ImportPath   string
	TestGoFiles  []string
	XTestGoFiles []string
	TestImports  []string
	XTestImports []string
}

type fixturePackage struct {
	ImportPath string
	Tests      []string
}

type functionCalls struct {
	usesFixture bool
	calls       []string
}

type testEvent struct {
	Action string
	Test   string
	Output string
}

func main() {
	list := flag.Bool("list", false, "list packages whose tests use the dbtest fixture")
	check := flag.Bool("check", false, "validate fixture package and test discovery without running tests")
	flag.Parse()
	if *list && *check {
		fatalf("--list and --check are mutually exclusive")
	}
	packages, err := discoverFixturePackages()
	if err != nil {
		fatalf("%v", err)
	}
	if *list {
		for _, pkg := range packages {
			fmt.Println("./" + pkg.ImportPath)
		}
		return
	}
	if *check {
		total := 0
		for _, pkg := range packages {
			total += len(pkg.Tests)
		}
		fmt.Printf("dbtest fixture discovery: %d packages, %d tests; every dbtest test importer has selected tests\n", len(packages), total)
		return
	}
	if err := runFixtureTests(packages); err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "test:database: "+format+"\n", args...)
	os.Exit(1)
}

func modulePath() (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}}")
	output, err := cmd.Output()
	if err != nil {
		return "", commandError("go list -m", err)
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return "", errors.New("go list -m returned an empty module path")
	}
	return path, nil
}

func discoverFixturePackages() ([]fixturePackage, error) {
	module, err := modulePath()
	if err != nil {
		return nil, err
	}
	dbtestImport := module + dbtestPackageSuffix
	cmd := exec.Command("go", "list", "-json", "./...")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start go list: %w", err)
	}
	decoder := json.NewDecoder(stdout)
	var packages []fixturePackage
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = cmd.Wait()
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if !slices.Contains(pkg.TestImports, dbtestImport) && !slices.Contains(pkg.XTestImports, dbtestImport) {
			continue
		}
		tests, err := fixtureTests(pkg, dbtestImport)
		if err != nil {
			_ = cmd.Wait()
			return nil, err
		}
		if len(tests) == 0 {
			_ = cmd.Wait()
			return nil, fmt.Errorf("%s imports dbtest in tests but no test reaches dbtest.NewFixture", pkg.ImportPath)
		}
		packages = append(packages, fixturePackage{
			ImportPath: strings.TrimPrefix(pkg.ImportPath, module+"/"),
			Tests:      tests,
		})
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("go list ./...: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if len(packages) == 0 {
		return nil, errors.New("no test packages import internal/dbtest")
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].ImportPath < packages[j].ImportPath })
	return packages, nil
}

func fixtureTests(pkg listedPackage, dbtestImport string) ([]string, error) {
	var tests []string
	for _, group := range []struct {
		files    []string
		external bool
	}{
		{files: pkg.TestGoFiles},
		{files: pkg.XTestGoFiles, external: true},
	} {
		functions := make(map[string]functionCalls)
		var candidates []string
		for _, name := range group.files {
			path := filepath.Join(pkg.Dir, name)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
			aliases := dbtestAliases(file, dbtestImport)
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				calls := inspectFunction(function, aliases)
				functions[functionKey(function)] = calls
				if function.Recv == nil && isTopLevelTest(function.Name.Name) {
					candidates = append(candidates, function.Name.Name)
				}
			}
		}
		for _, name := range candidates {
			if reachesFixture(name, functions, make(map[string]bool)) {
				tests = append(tests, name)
			}
		}
	}
	sort.Strings(tests)
	return slices.Compact(tests), nil
}

func dbtestAliases(file *ast.File, dbtestImport string) map[string]bool {
	aliases := make(map[string]bool)
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, "\"")
		if path != dbtestImport {
			continue
		}
		alias := "dbtest"
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		if alias != "_" && alias != "." {
			aliases[alias] = true
		}
	}
	return aliases
}

func functionKey(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return function.Name.Name
	}
	return namedType(function.Recv.List[0].Type) + "." + function.Name.Name
}

func inspectFunction(function *ast.FuncDecl, aliases map[string]bool) functionCalls {
	var result functionCalls
	seenCalls := make(map[string]bool)
	valueTypes := localValueTypes(function)
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		var called string
		switch function := call.Fun.(type) {
		case *ast.Ident:
			called = function.Name
		case *ast.SelectorExpr:
			identifier, ok := function.X.(*ast.Ident)
			if ok && aliases[identifier.Name] && function.Sel.Name == "NewFixture" {
				result.usesFixture = true
			}
			if receiver := expressionType(function.X, valueTypes); receiver != "" {
				called = receiver + "." + function.Sel.Name
			}
		}
		if called != "" && !seenCalls[called] {
			result.calls = append(result.calls, called)
			seenCalls[called] = true
		}
		return true
	})
	return result
}

func localValueTypes(function *ast.FuncDecl) map[*ast.Object]string {
	result := make(map[*ast.Object]string)
	addFields := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			typeName := namedType(field.Type)
			if typeName == "" {
				continue
			}
			for _, name := range field.Names {
				if name.Obj != nil {
					result[name.Obj] = typeName
				}
			}
		}
	}
	addFields(function.Recv)
	addFields(function.Type.Params)
	addFields(function.Type.Results)
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.ValueSpec:
			for index, name := range node.Names {
				typeName := namedType(node.Type)
				if typeName == "" && index < len(node.Values) {
					typeName = expressionType(node.Values[index], result)
				}
				if typeName != "" && name.Obj != nil {
					result[name.Obj] = typeName
				}
			}
		case *ast.AssignStmt:
			if len(node.Lhs) != len(node.Rhs) {
				return true
			}
			for index, left := range node.Lhs {
				name, ok := left.(*ast.Ident)
				if !ok || name.Obj == nil {
					continue
				}
				if typeName := expressionType(node.Rhs[index], result); typeName != "" {
					result[name.Obj] = typeName
				}
			}
		}
		return true
	})
	return result
}

func namedType(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.StarExpr:
		return namedType(expression.X)
	case *ast.IndexExpr:
		return namedType(expression.X)
	case *ast.IndexListExpr:
		return namedType(expression.X)
	default:
		return ""
	}
}

func expressionType(expression ast.Expr, valueTypes map[*ast.Object]string) string {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		return expressionType(expression.X, valueTypes)
	case *ast.Ident:
		return valueTypes[expression.Obj]
	case *ast.CompositeLit:
		return namedType(expression.Type)
	case *ast.UnaryExpr:
		return expressionType(expression.X, valueTypes)
	case *ast.CallExpr:
		function, ok := expression.Fun.(*ast.Ident)
		if ok && function.Name == "new" && len(expression.Args) == 1 {
			return namedType(expression.Args[0])
		}
	}
	return ""
}

func isTopLevelTest(name string) bool {
	if !strings.HasPrefix(name, "Test") || len(name) == len("Test") {
		return false
	}
	next := name[len("Test")]
	return next < 'a' || next > 'z'
}

func reachesFixture(name string, functions map[string]functionCalls, visiting map[string]bool) bool {
	if visiting[name] {
		return false
	}
	function, ok := functions[name]
	if !ok {
		return false
	}
	if function.usesFixture {
		return true
	}
	visiting[name] = true
	defer delete(visiting, name)
	for _, called := range function.calls {
		if reachesFixture(called, functions, visiting) {
			return true
		}
	}
	return false
}

func runFixtureTests(packages []fixturePackage) error {
	backend, err := selectedBackend()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "database backend: %s\n", backend)
	for _, pkg := range packages {
		fmt.Fprintf(os.Stderr, "database package: ./%s (%d fixture tests)\n", pkg.ImportPath, len(pkg.Tests))
		if err := runPackage(pkg, backend); err != nil {
			return err
		}
	}
	// Suite-level audit: runPackage only returns nil after the executed tests
	// match the discovered selection exactly (no skips, no missing fixture
	// uses), so these totals reconcile discovery with execution for the leg.
	totalTests := 0
	for _, pkg := range packages {
		totalTests += len(pkg.Tests)
	}
	fmt.Fprintf(os.Stderr, "database suite audit: %d packages, %d fixture tests executed and fixture-audited on backend %s\n", len(packages), totalTests, backend)
	return nil
}

func selectedBackend() (string, error) {
	raw := strings.TrimSpace(os.Getenv("NEXTERM_TEST_DB_BACKEND"))
	if raw == "" {
		fmt.Fprintln(os.Stderr, "WARNING: NEXTERM_TEST_DB_BACKEND is empty; explicitly falling back to SQLite")
		raw = "sqlite"
	}
	var backend string
	switch strings.ToLower(raw) {
	case "sqlite":
		backend = "sqlite"
	case "postgres", "postgresql":
		backend = "postgres"
	case "mysql":
		backend = "mysql"
	case "mariadb":
		backend = "mariadb"
	default:
		return "", fmt.Errorf("invalid NEXTERM_TEST_DB_BACKEND %q (want sqlite, postgres, mysql or mariadb)", raw)
	}
	if backend != "sqlite" && strings.TrimSpace(os.Getenv("NEXTERM_TEST_DB_DSN")) == "" {
		return "", fmt.Errorf("NEXTERM_TEST_DB_DSN is required for the selected %s backend", backend)
	}
	return backend, nil
}

func runPackage(pkg fixturePackage, backend string) error {
	report, err := os.CreateTemp("", "nexterm-dbtest-report-*.jsonl")
	if err != nil {
		return fmt.Errorf("create fixture report for %s: %w", pkg.ImportPath, err)
	}
	reportPath := report.Name()
	if err := report.Close(); err != nil {
		_ = os.Remove(reportPath)
		return fmt.Errorf("close fixture report for %s: %w", pkg.ImportPath, err)
	}
	defer func() { _ = os.Remove(reportPath) }()

	quoted := make([]string, len(pkg.Tests))
	selected := make(map[string]bool, len(pkg.Tests))
	for i, name := range pkg.Tests {
		quoted[i] = regexp.QuoteMeta(name)
		selected[name] = true
	}
	args := []string{"test", "-json", "-count=1", "-run", "^(" + strings.Join(quoted, "|") + ")$", "./" + pkg.ImportPath}
	cmd := exec.Command("go", args...)
	cmd.Env = environmentWith("NEXTERM_TEST_DB_BACKEND", backend)
	cmd.Env = environmentWithValue(cmd.Env, "NEXTERM_DBTEST_REPORT", reportPath)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start go test for %s: %w", pkg.ImportPath, err)
	}
	executed := make(map[string]bool)
	skipped := make(map[string]bool)
	decoder := json.NewDecoder(stdout)
	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = cmd.Wait()
			return fmt.Errorf("parse go test -json output for %s: %w", pkg.ImportPath, err)
		}
		if event.Output != "" {
			fmt.Print(event.Output)
		}
		if event.Action == "run" && selected[event.Test] {
			executed[event.Test] = true
		}
		if event.Action == "skip" && event.Test != "" {
			skipped[event.Test] = true
		}
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return fmt.Errorf("go test ./%s failed: %w", pkg.ImportPath, waitErr)
	}
	if err := validateSkippedTests(pkg, skipped); err != nil {
		return err
	}
	if err := validateExecutedTests(pkg, executed); err != nil {
		return err
	}
	fixtureUses, err := readFixtureUseReport(reportPath)
	if err != nil {
		return fmt.Errorf("read fixture report for %s: %w", pkg.ImportPath, err)
	}
	if err := validateFixtureUse(pkg, fixtureUses, backend); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "database package passed: ./%s (%d tests executed and fixture-audited)\n", pkg.ImportPath, len(pkg.Tests))
	return nil
}

func validateSkippedTests(pkg fixturePackage, skipped map[string]bool) error {
	selected := make(map[string]bool, len(pkg.Tests))
	for _, name := range pkg.Tests {
		selected[name] = true
	}
	var names []string
	for name := range skipped {
		if selected[topLevelTestName(name)] {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		return fmt.Errorf("go test ./%s skipped selected fixture tests: %s", pkg.ImportPath, strings.Join(names, ", "))
	}
	return nil
}

func validateExecutedTests(pkg fixturePackage, executed map[string]bool) error {
	var missing []string
	for _, name := range pkg.Tests {
		if !executed[name] {
			missing = append(missing, name)
		}
	}
	if len(executed) == 0 || len(missing) > 0 {
		return fmt.Errorf("go test ./%s executed %d of %d selected fixture tests; missing: %s", pkg.ImportPath, len(executed), len(pkg.Tests), strings.Join(missing, ", "))
	}
	return nil
}

type fixtureUse struct {
	Test    string `json:"test"`
	Backend string `json:"backend"`
}

func readFixtureUseReport(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	uses := make(map[string]string)
	decoder := json.NewDecoder(file)
	for {
		var use fixtureUse
		if err := decoder.Decode(&use); err != nil {
			if errors.Is(err, io.EOF) {
				return uses, nil
			}
			return nil, err
		}
		name := topLevelTestName(use.Test)
		if name == "" || use.Backend == "" {
			return nil, errors.New("fixture report contains an entry without a test or backend")
		}
		if previous, ok := uses[name]; ok && previous != use.Backend {
			return nil, fmt.Errorf("fixture report contains conflicting backends for %s", name)
		}
		uses[name] = use.Backend
	}
}

func topLevelTestName(name string) string {
	if index := strings.IndexByte(name, '/'); index >= 0 {
		return name[:index]
	}
	return name
}

func validateFixtureUse(pkg fixturePackage, uses map[string]string, backend string) error {
	var missing []string
	var wrongBackend []string
	for _, name := range pkg.Tests {
		actual, ok := uses[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		if actual != backend {
			wrongBackend = append(wrongBackend, fmt.Sprintf("%s (got %s)", name, actual))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("go test ./%s has selected tests without fixture use: %s", pkg.ImportPath, strings.Join(missing, ", "))
	}
	if len(wrongBackend) > 0 {
		return fmt.Errorf("go test ./%s used the wrong fixture backend (want %s): %s", pkg.ImportPath, backend, strings.Join(wrongBackend, ", "))
	}
	return nil
}

func environmentWith(key, value string) []string {
	return environmentWithValue(os.Environ(), key, value)
}

func environmentWithValue(environment []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			environment[i] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

func commandError(name string, err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(exit.Stderr)))
	}
	return fmt.Errorf("%s: %w", name, err)
}
