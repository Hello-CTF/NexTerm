package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerationRemovesDeletedModelQueryWithoutTouchingOtherFiles(t *testing.T) {
	root := t.TempDir()
	query := filepath.Join(root, generatedQueryPath)
	writeTestFile(t, filepath.Join(query, "gen.go"), "old registry")
	writeTestFile(t, filepath.Join(query, "kept.gen.go"), "old query")
	writeTestFile(t, filepath.Join(query, "deleted.gen.go"), "deleted model query")
	unregisteredModel := filepath.Join(root, "internal/store/model/unregistered.go")
	writeTestFile(t, unregisteredModel, "package model\n")
	extraFile := filepath.Join(root, "internal/store/extra.txt")
	writeTestFile(t, extraFile, "keep")
	unrelatedScratch := filepath.Join(root, ".tmp/gorm-unrelated/keep.txt")
	writeTestFile(t, unrelatedScratch, "keep scratch")

	generate := func(output string) error {
		writeTestFile(t, filepath.Join(output, "gen.go"), "new registry")
		writeTestFile(t, filepath.Join(output, "kept.gen.go"), "new query")
		return nil
	}
	if err := runGeneration(root, false, generate); err != nil {
		t.Fatalf("runGeneration() error = %v", err)
	}

	assertTestFile(t, filepath.Join(query, "gen.go"), "new registry")
	assertTestFile(t, filepath.Join(query, "kept.gen.go"), "new query")
	if _, err := os.Stat(filepath.Join(query, "deleted.gen.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted model query still exists or stat failed unexpectedly: %v", err)
	}
	assertTestFile(t, unregisteredModel, "package model\n")
	assertTestFile(t, extraFile, "keep")
	assertTestFile(t, unrelatedScratch, "keep scratch")
}

func TestCheckReportsDriftAndOrphanOutputs(t *testing.T) {
	root := t.TempDir()
	query := filepath.Join(root, generatedQueryPath)
	writeTestFile(t, filepath.Join(query, "gen.go"), "registry")
	writeTestFile(t, filepath.Join(query, "changed.gen.go"), "committed query")
	writeTestFile(t, filepath.Join(query, "orphan.gen.go"), "stale query")

	generate := func(output string) error {
		writeTestFile(t, filepath.Join(output, "gen.go"), "registry")
		writeTestFile(t, filepath.Join(output, "changed.gen.go"), "regenerated query")
		return nil
	}
	err := runGeneration(root, true, generate)
	if err == nil {
		t.Fatal("runGeneration() succeeded with drift and an orphan")
	}
	message := err.Error()
	for _, want := range []string{
		"changed.gen.go: content differs",
		"orphan.gen.go: only in committed query (orphan output)",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("runGeneration() error %q does not contain %q", message, want)
		}
	}
	assertTestFile(t, filepath.Join(query, "changed.gen.go"), "committed query")
	assertTestFile(t, filepath.Join(query, "orphan.gen.go"), "stale query")
}

func TestGenerationFailurePreservesCommittedQuery(t *testing.T) {
	root := t.TempDir()
	query := filepath.Join(root, generatedQueryPath)
	writeTestFile(t, filepath.Join(query, "gen.go"), "original registry")
	writeTestFile(t, filepath.Join(query, "model.gen.go"), "original query")
	writeTestFile(t, filepath.Join(query, "orphan.gen.go"), "original orphan")

	generate := func(output string) error {
		writeTestFile(t, filepath.Join(output, "gen.go"), "partial registry")
		return errors.New("injected generation failure")
	}
	err := runGeneration(root, false, generate)
	if err == nil || !strings.Contains(err.Error(), "injected generation failure") {
		t.Fatalf("runGeneration() error = %v, want injected generation failure", err)
	}

	assertTestFile(t, filepath.Join(query, "gen.go"), "original registry")
	assertTestFile(t, filepath.Join(query, "model.gen.go"), "original query")
	assertTestFile(t, filepath.Join(query, "orphan.gen.go"), "original orphan")
}

func TestGenerationInstallFailureRestoreSuccessCleansScratch(t *testing.T) {
	root, committed := replacementFixture(t)
	installErr := errors.New("injected install failure")
	var backup, temporary string
	calls := 0
	rename := func(oldpath, newpath string) error {
		calls++
		if calls == 1 {
			backup = newpath
			temporary = filepath.Dir(newpath)
		}
		if calls == 2 {
			return installErr
		}
		return os.Rename(oldpath, newpath)
	}

	err := runGenerationWithRename(root, false, replacementGenerator(t), rename)
	if !errors.Is(err, installErr) {
		t.Fatalf("runGenerationWithRename() error = %v, want install failure", err)
	}
	if backup == "" {
		t.Fatal("backup rename was not observed")
	}
	assertTestFile(t, filepath.Join(committed, "gen.go"), "original")
	assertTestMissing(t, backup)
	assertTestMissing(t, temporary)
}

func TestGenerationInstallAndRestoreFailurePreserveBackup(t *testing.T) {
	root, committed := replacementFixture(t)
	installErr := errors.New("injected install failure")
	restoreErr := errors.New("injected restore failure")
	var backup string
	calls := 0
	rename := func(oldpath, newpath string) error {
		calls++
		if calls == 1 {
			backup = newpath
		}
		if calls == 2 {
			return installErr
		}
		if calls == 3 {
			return restoreErr
		}
		return os.Rename(oldpath, newpath)
	}

	err := runGenerationWithRename(root, false, replacementGenerator(t), rename)
	if !errors.Is(err, installErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("runGenerationWithRename() error = %v, want install and restore failures", err)
	}
	if backup == "" {
		t.Fatal("backup rename was not observed")
	}
	for _, want := range []string{backup, committed, "backup preserved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("runGenerationWithRename() error %q does not contain %q", err, want)
		}
	}
	assertTestFile(t, filepath.Join(backup, "gen.go"), "original")
	assertTestMissing(t, committed)
}

func TestGenerationSuccessRemovesBackup(t *testing.T) {
	root, committed := replacementFixture(t)
	var backup, temporary string
	calls := 0
	rename := func(oldpath, newpath string) error {
		calls++
		if calls == 1 {
			backup = newpath
			temporary = filepath.Dir(newpath)
		}
		return os.Rename(oldpath, newpath)
	}

	if err := runGenerationWithRename(root, false, replacementGenerator(t), rename); err != nil {
		t.Fatalf("runGenerationWithRename() error = %v", err)
	}
	if backup == "" {
		t.Fatal("backup rename was not observed")
	}
	assertTestFile(t, filepath.Join(committed, "gen.go"), "regenerated")
	assertTestMissing(t, backup)
	assertTestMissing(t, temporary)
}

func TestGenerationConcurrentOccupationPreservesBackupAndOccupant(t *testing.T) {
	root, committed := replacementFixture(t)
	var backup string
	calls := 0
	rename := func(oldpath, newpath string) error {
		calls++
		if calls == 1 {
			backup = newpath
		}
		if calls == 2 {
			writeTestFile(t, filepath.Join(committed, "occupant.txt"), "other writer")
		}
		return os.Rename(oldpath, newpath)
	}

	err := runGenerationWithRename(root, false, replacementGenerator(t), rename)
	if err == nil {
		t.Fatal("runGenerationWithRename() succeeded despite an occupied destination")
	}
	if backup == "" {
		t.Fatal("backup rename was not observed")
	}
	if !strings.Contains(err.Error(), "backup preserved") || !strings.Contains(err.Error(), backup) {
		t.Errorf("runGenerationWithRename() error %q does not identify the preserved backup %q", err, backup)
	}
	assertTestFile(t, filepath.Join(backup, "gen.go"), "original")
	assertTestFile(t, filepath.Join(committed, "occupant.txt"), "other writer")
}

func TestGenerationPanicAfterBackupPreservesBackup(t *testing.T) {
	root, committed := replacementFixture(t)
	const panicValue = "injected rename panic"
	var backup string
	calls := 0
	rename := func(oldpath, newpath string) error {
		calls++
		if calls == 1 {
			backup = newpath
		}
		if calls == 2 {
			panic(panicValue)
		}
		return os.Rename(oldpath, newpath)
	}

	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Errorf("runGenerationWithRename() panic = %v, want %q", recovered, panicValue)
			}
		}()
		_ = runGenerationWithRename(root, false, replacementGenerator(t), rename)
	}()
	if backup == "" {
		t.Fatal("backup rename was not observed")
	}
	assertTestFile(t, filepath.Join(backup, "gen.go"), "original")
	assertTestMissing(t, committed)
}

func replacementFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	committed := filepath.Join(root, generatedQueryPath)
	writeTestFile(t, filepath.Join(committed, "gen.go"), "original")
	return root, committed
}

func replacementGenerator(t *testing.T) func(string) error {
	t.Helper()
	return func(output string) error {
		writeTestFile(t, filepath.Join(output, "gen.go"), "regenerated")
		return nil
	}
}

func assertTestMissing(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%q still exists or stat failed unexpectedly: %v", name, err)
	}
}

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", name, err)
	}
}

func assertTestFile(t *testing.T, name, want string) {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", name, err)
	}
	if string(content) != want {
		t.Errorf("%q content = %q, want %q", name, content, want)
	}
}
