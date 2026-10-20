package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCompileOnlyDoesNotRunTestMainOrTests(t *testing.T) {
	markerDir := t.TempDir()
	testMainMarker := filepath.Join(markerDir, "test-main-ran")
	testMarker := filepath.Join(markerDir, "test-ran")
	module := writeFixtureModule(t, map[string]string{
		"withmain/withmain.go": `package withmain
func Value() int { return 1 }
`,
		"withmain/withmain_test.go": fmt.Sprintf(`package withmain
import (
    "os"
    "testing"
)
func TestMain(m *testing.M) {
    if err := os.WriteFile(%q, []byte("ran"), 0o600); err != nil { panic(err) }
    os.Exit(97)
}
`, testMainMarker),
		"external/external.go": `package external
func Value() int { return 1 }
`,
		"external/external_test.go": fmt.Sprintf(`package external_test
import (
    "os"
    "testing"
)
func TestMustNotRun(t *testing.T) {
    if err := os.WriteFile(%q, []byte("ran"), 0o600); err != nil { t.Fatal(err) }
    os.Exit(98)
}
`, testMarker),
		"plain/plain.go": `package main
func Value() int { return 1 }
`,
	})
	artifactRoot := t.TempDir()
	output, err := runFixture(module, artifactRoot, nil)
	if err != nil {
		t.Fatalf("compile-only failed: %v\n%s", err, output)
	}
	for _, marker := range []string{testMainMarker, testMarker} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("test execution marker %s exists or could not be checked: %v", marker, err)
		}
	}
	for _, summary := range []string{"test packages compiled=2", "non-test packages checked=1", "test binaries executed=0"} {
		if !strings.Contains(output, summary) {
			t.Fatalf("output %q does not contain %q", output, summary)
		}
	}
	assertEmptyDir(t, artifactRoot)
	assertNoBuildArtifacts(t, module)
}

func TestCompileOnlyCompilesPackagesWithoutTests(t *testing.T) {
	module := writeFixtureModule(t, map[string]string{
		"plain/plain.go": `package plain
var Value = compileOnlyPlainMissingSymbol
`,
	})
	artifactRoot := t.TempDir()
	output, err := runFixture(module, artifactRoot, nil)
	if err == nil {
		t.Fatalf("a package without tests compiled despite its error:\n%s", output)
	}
	if !strings.Contains(err.Error(), "example.com/fixture/plain") {
		t.Fatalf("error %q does not identify the non-test package", err)
	}
	if !strings.Contains(output, "compileOnlyPlainMissingSymbol") {
		t.Fatalf("compiler output was not preserved:\n%s", output)
	}
	assertEmptyDir(t, artifactRoot)
	assertNoBuildArtifacts(t, module)
}

func TestCompileOnlyPreservesTestCompilerErrors(t *testing.T) {
	module := writeFixtureModule(t, map[string]string{
		"broken/broken.go": `package broken
func Value() int { return 1 }
`,
		"broken/broken_test.go": `package broken
import "testing"
func TestCompileError(t *testing.T) {
    _ = compileOnlyTestMissingSymbol
}
`,
	})
	artifactRoot := t.TempDir()
	output, err := runFixture(module, artifactRoot, nil)
	if err == nil {
		t.Fatalf("a broken test package compiled successfully:\n%s", output)
	}
	if !strings.Contains(err.Error(), "example.com/fixture/broken") {
		t.Fatalf("error %q does not identify the test package", err)
	}
	if !strings.Contains(output, "compileOnlyTestMissingSymbol") {
		t.Fatalf("compiler output was not preserved:\n%s", output)
	}
	assertEmptyDir(t, artifactRoot)
	assertNoBuildArtifacts(t, module)
}

func TestCompileOnlyWindowsOutputsAreCleaned(t *testing.T) {
	module := writeFixtureModule(t, map[string]string{
		"first/first.go": `package same
func First() int { return 1 }
`,
		"first/first_test.go": `package same
import "testing"
func TestFirst(t *testing.T) {}
`,
		"second/second.go": `package same
func Second() int { return 2 }
`,
		"second/second_test.go": `package same_test
import "testing"
func TestSecond(t *testing.T) {}
`,
		"command/main.go": `package main
func main() {}
`,
	})
	artifactRoot := t.TempDir()
	output, err := runFixture(module, artifactRoot, map[string]string{
		"GOOS":        "windows",
		"GOARCH":      "amd64",
		"CGO_ENABLED": "0",
	})
	if err != nil {
		t.Fatalf("Windows cross-compilation failed: %v\n%s", err, output)
	}
	for _, summary := range []string{"test packages compiled=2", "non-test packages checked=1"} {
		if !strings.Contains(output, summary) {
			t.Fatalf("output %q does not contain %q", output, summary)
		}
	}
	assertEmptyDir(t, artifactRoot)
	assertNoBuildArtifacts(t, module)
}

func TestCompileOnlyPreservesBuildTags(t *testing.T) {
	module := writeFixtureModule(t, map[string]string{
		"feature/feature.go": `package feature
func Value() int { return 1 }
`,
		"feature/tagged_test.go": `//go:build special

package feature
import "testing"
func TestTagged(t *testing.T) {}
`,
	})
	output, err := runFixture(module, t.TempDir(), map[string]string{
		"GOFLAGS": "-tags=special -mod=readonly",
	})
	if err != nil {
		t.Fatalf("tagged test compilation failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "test packages compiled=1") {
		t.Fatalf("tagged test package was not discovered:\n%s", output)
	}
}

func TestCompileOnlyForcesReadonlyModules(t *testing.T) {
	module := writeFixtureModule(t, map[string]string{
		"go.mod": `module example.com/fixture

go 1.26.0

replace example.com/dep => ./dep
`,
		"consumer/consumer.go": `package consumer
import "example.com/dep"
var Value = dep.Value
`,
		"dep/go.mod": `module example.com/dep

go 1.26.0
`,
		"dep/dep.go": `package dep
var Value = 1
`,
	})
	goModBefore, err := os.ReadFile(filepath.Join(module, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := runFixture(module, t.TempDir(), map[string]string{
		"GOFLAGS": "-mod=mod",
	})
	if err == nil {
		t.Fatalf("readonly module compilation unexpectedly succeeded:\n%s", output)
	}
	if !strings.Contains(output, "example.com/dep") {
		t.Fatalf("readonly module error was not preserved:\n%s", output)
	}
	goModAfter, readErr := os.ReadFile(filepath.Join(module, "go.mod"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(goModAfter, goModBefore) {
		t.Fatalf("go.mod changed during readonly compilation:\n%s", goModAfter)
	}
}

func runFixture(module, artifactRoot string, overrides map[string]string) (string, error) {
	environment := map[string]string{
		"CGO_ENABLED": "0",
		"GOFLAGS":     "-mod=readonly",
		"GOTOOLCHAIN": "local",
		"GOWORK":      "off",
	}
	for key, value := range overrides {
		environment[key] = value
	}
	var output bytes.Buffer
	err := run(context.Background(), []string{"-p=1", "./..."}, runOptions{
		dir:      module,
		env:      replaceEnvironment(os.Environ(), environment),
		stdout:   &output,
		stderr:   &output,
		tempRoot: artifactRoot,
	})
	return output.String(), err
}

func writeFixtureModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/fixture\n\ngo 1.26.0\n"
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func replaceEnvironment(base []string, overrides map[string]string) []string {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := append([]string(nil), base...)
	for _, key := range keys {
		prefix := key + "="
		kept := result[:0]
		for _, entry := range result {
			if !strings.HasPrefix(entry, prefix) {
				kept = append(kept, entry)
			}
		}
		result = append(kept, prefix+overrides[key])
	}
	return result
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("temporary compile outputs were not cleaned: %v", names)
	}
}

func assertNoBuildArtifacts(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		switch filepath.Ext(entry.Name()) {
		case ".a", ".build", ".exe", ".o", ".out", ".test":
			t.Fatalf("build artifact remains in module: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
