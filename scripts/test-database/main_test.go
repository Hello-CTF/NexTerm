package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFixtureTestsFollowTestHelperCalls(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"fixture_test.go": `package sample
import (
    "testing"
    "example.com/internal/dbtest"
)
func openFixture(t *testing.T) { dbtest.NewFixture(t) }
func TestDirectFixture(t *testing.T) { dbtest.NewFixture(t) }
type fixtureMethodHelper struct{}
func (*fixtureMethodHelper) open(t *testing.T) { openFixture(t) }
type unrelatedMethodHelper struct{}
func (*unrelatedMethodHelper) open(t *testing.T) {}
`,
		"contracts_test.go": `package sample
import "testing"
func callFixtureAgain(t *testing.T) { openFixture(t) }
func TestIndirectFixture(t *testing.T) { callFixtureAgain(t) }
func TestMethodFixture(t *testing.T) { helper := &fixtureMethodHelper{}; helper.open(t) }
func TestConstructedMethodFixture(t *testing.T) { new(fixtureMethodHelper).open(t) }
func TestUnrelatedMethod(t *testing.T) { helper := &unrelatedMethodHelper{}; helper.open(t) }
func TestShadowedUnrelatedMethod(t *testing.T) {
    helper := &unrelatedMethodHelper{}
    { helper := &fixtureMethodHelper{}; _ = helper }
    helper.open(t)
}
func TestSQLiteOnly(t *testing.T) {}
`,
	}
	var names []string
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	tests, err := fixtureTests(listedPackage{Dir: directory, TestGoFiles: names}, "example.com/internal/dbtest")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"TestConstructedMethodFixture", "TestDirectFixture", "TestIndirectFixture", "TestMethodFixture"}
	if !reflect.DeepEqual(tests, want) {
		t.Fatalf("selected tests = %v, want %v", tests, want)
	}
}

func TestValidateExecutedTestsRejectsZeroOrMissingTests(t *testing.T) {
	pkg := fixturePackage{ImportPath: "internal/example", Tests: []string{"TestOne", "TestTwo"}}
	if err := validateExecutedTests(pkg, map[string]bool{}); err == nil {
		t.Fatal("zero executed tests passed validation")
	}
	if err := validateExecutedTests(pkg, map[string]bool{"TestOne": true}); err == nil {
		t.Fatal("a missing selected test passed validation")
	}
	if err := validateExecutedTests(pkg, map[string]bool{"TestOne": true, "TestTwo": true}); err != nil {
		t.Fatalf("all selected tests executed: %v", err)
	}
}

func TestValidateSkippedTestsRejectsOnlySelectedContracts(t *testing.T) {
	pkg := fixturePackage{ImportPath: "internal/example", Tests: []string{"TestSelected", "TestAlsoSelected"}}
	if err := validateSkippedTests(pkg, map[string]bool{"TestUnrelated": true, "TestAnother/child": true}); err != nil {
		t.Fatalf("unrelated skips failed validation: %v", err)
	}
	for _, name := range []string{"TestSelected", "TestAlsoSelected/child"} {
		err := validateSkippedTests(pkg, map[string]bool{name: true})
		if err == nil {
			t.Fatalf("selected skip %s passed validation", name)
		}
		if !strings.Contains(err.Error(), "internal/example") || !strings.Contains(err.Error(), name) {
			t.Fatalf("selected skip error %q does not identify package and test", err)
		}
	}
	if err := validateSkippedTests(pkg, nil); err != nil {
		t.Fatalf("no skips failed validation: %v", err)
	}
}

func TestValidateFixtureUseRejectsZeroMissingAndWrongBackend(t *testing.T) {
	pkg := fixturePackage{ImportPath: "internal/example", Tests: []string{"TestOne", "TestTwo"}}
	tests := []struct {
		name     string
		uses     map[string]string
		wantTest string
	}{
		{name: "zero fixtures", wantTest: "TestOne"},
		{name: "missing fixture", uses: map[string]string{"TestOne": "sqlite", "TestUnrelated": "sqlite"}, wantTest: "TestTwo"},
		{name: "wrong fixture backend", uses: map[string]string{"TestOne": "postgres", "TestTwo": "sqlite"}, wantTest: "TestOne"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateFixtureUse(pkg, test.uses, "sqlite")
			if err == nil {
				t.Fatal("invalid fixture use passed validation")
			}
			if !strings.Contains(err.Error(), "internal/example") || !strings.Contains(err.Error(), test.wantTest) {
				t.Fatalf("fixture use error %q does not identify package and test", err)
			}
		})
	}
	if err := validateFixtureUse(pkg, map[string]string{"TestOne": "sqlite", "TestTwo": "sqlite"}, "sqlite"); err != nil {
		t.Fatalf("valid fixture use failed validation: %v", err)
	}
}

func TestReadFixtureUseReportGroupsSubtests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixtures.jsonl")
	contents := "{\"test\":\"TestOne/first\",\"backend\":\"sqlite\"}\n" +
		"{\"test\":\"TestOne/second\",\"backend\":\"sqlite\"}\n" +
		"{\"test\":\"TestTwo\",\"backend\":\"sqlite\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	uses, err := readFixtureUseReport(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"TestOne": "sqlite", "TestTwo": "sqlite"}
	if !reflect.DeepEqual(uses, want) {
		t.Fatalf("fixture uses = %v, want %v", uses, want)
	}
}

func TestReadFixtureUseReportRejectsConflictingBackends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixtures.jsonl")
	contents := "{\"test\":\"TestOne/first\",\"backend\":\"sqlite\"}\n" +
		"{\"test\":\"TestOne/second\",\"backend\":\"mysql\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	uses, err := readFixtureUseReport(path)
	if err == nil {
		t.Fatalf("conflicting fixture uses passed validation: %v", uses)
	}
	if !strings.Contains(err.Error(), "TestOne") {
		t.Fatalf("conflicting backend error %q does not identify test", err)
	}
}

func TestFixtureTestsHandleHelperCycles(t *testing.T) {
	directory := t.TempDir()
	contents := `package sample
import (
    "testing"
    fixture "example.com/internal/dbtest"
)
func first(t *testing.T) { second(t) }
func second(t *testing.T) { first(t); fixture.NewFixture(t) }
func TestCycle(t *testing.T) { first(t) }
`
	if err := os.WriteFile(filepath.Join(directory, "cycle_test.go"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	tests, err := fixtureTests(listedPackage{Dir: directory, TestGoFiles: []string{"cycle_test.go"}}, "example.com/internal/dbtest")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tests, []string{"TestCycle"}) {
		t.Fatalf("selected tests = %v", tests)
	}
}

func TestRunFixtureTestsPrintsSuiteAuditReconcilingDiscoveryAndExecution(t *testing.T) {
	t.Chdir("../..")
	if err := os.MkdirAll(".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(".tmp", "database-suite-audit-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(directory) }()
	contents := `package suiteauditfixture
import (
    "testing"
    "github.com/Hello-CTF/NexTerm/internal/dbtest"
)
func TestPositive(t *testing.T) { dbtest.NewFixture(t).OpenStore(t) }
`
	if err := os.WriteFile(filepath.Join(directory, "audit_test.go"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	packages := []fixturePackage{{ImportPath: filepath.ToSlash(directory), Tests: []string{"TestPositive"}}}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	os.Stderr = writer
	runErr := runFixtureTests(packages)
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	os.Stderr = originalStderr
	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("full-suite run failed: %v\n%s", runErr, captured)
	}
	for _, want := range []string{
		"database suite audit: 1 packages, 1 fixture tests executed and fixture-audited on backend sqlite",
	} {
		if !strings.Contains(string(captured), want) {
			t.Fatalf("suite audit output %q does not contain %q", captured, want)
		}
	}
}

func TestRunPackageAuditsExecutedContracts(t *testing.T) {
	t.Chdir("../..")
	if err := os.MkdirAll(".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(".tmp", "database-runner-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(directory) }()
	contents := `package runnerfixture
import (
    "testing"
    "github.com/Hello-CTF/NexTerm/internal/dbtest"
)
func TestPositive(t *testing.T) { dbtest.NewFixture(t).OpenStore(t) }
func TestNewFixtureOnly(t *testing.T) { dbtest.NewFixture(t) }
func TestSelectedSkip(t *testing.T) { t.Skip("deterministic selected skip") }
func TestZeroFixture(t *testing.T) {}
func TestWrongBackend(t *testing.T) {
    t.Setenv("NEXTERM_TEST_DB_BACKEND", "sqlite")
    dbtest.NewFixture(t).OpenStore(t)
}
func TestOpenFailure(t *testing.T) {
    fixture := &dbtest.Fixture{Backend: "invalid", DSN: "unused"}
    fixture.OpenStore(t)
}
func TestReportFailure(t *testing.T) {
    t.Setenv("NEXTERM_DBTEST_REPORT", t.TempDir())
    dbtest.NewFixture(t).OpenStore(t)
}
`
	if err := os.WriteFile(filepath.Join(directory, "runner_test.go"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		scenario      string
		testName      string
		backend       string
		wantFail      bool
		goTestFailure bool
	}{
		{scenario: "positive", testName: "TestPositive", backend: "sqlite"},
		{scenario: "NewFixture without OpenStore", testName: "TestNewFixtureOnly", backend: "sqlite", wantFail: true},
		{scenario: "selected skip", testName: "TestSelectedSkip", backend: "sqlite", wantFail: true},
		{scenario: "zero fixture", testName: "TestZeroFixture", backend: "sqlite", wantFail: true},
		{scenario: "wrong backend", testName: "TestWrongBackend", backend: "mysql", wantFail: true},
		{scenario: "open failure", testName: "TestOpenFailure", backend: "sqlite", wantFail: true, goTestFailure: true},
		{scenario: "report failure", testName: "TestReportFailure", backend: "sqlite", wantFail: true, goTestFailure: true},
	}
	for _, test := range tests {
		t.Run(test.scenario, func(t *testing.T) {
			pkg := fixturePackage{ImportPath: filepath.ToSlash(directory), Tests: []string{test.testName}}
			err := runPackage(pkg, test.backend)
			if !test.wantFail {
				if err != nil {
					t.Fatalf("valid executed contract failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid executed contract passed")
			}
			if test.goTestFailure {
				if !strings.Contains(err.Error(), "go test ") || !strings.Contains(err.Error(), "failed") {
					t.Fatalf("runner error %q does not preserve the go test failure", err)
				}
				return
			}
			if !strings.Contains(err.Error(), pkg.ImportPath) || !strings.Contains(err.Error(), test.testName) {
				t.Fatalf("runner error %q does not identify package and test", err)
			}
		})
	}
}
