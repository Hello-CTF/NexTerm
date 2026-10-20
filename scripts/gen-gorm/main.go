package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gen"
)

const generatedQueryPath = "internal/store/query"

func main() {
	if len(os.Args) > 2 || len(os.Args) == 2 && os.Args[1] != "--check" {
		log.Fatal("usage: go run ./scripts/gen-gorm [--check]")
	}
	check := len(os.Args) == 2
	root, err := os.Getwd()
	if err != nil {
		log.Fatalf("determine working directory: %v", err)
	}
	if err := runGeneration(root, check, generateQuery); err != nil {
		log.Fatal(err)
	}
	if check {
		fmt.Println("GORM query: no drift; no orphan outputs")
	} else {
		fmt.Println("GORM query regenerated; stale outputs removed")
	}
}

func runGeneration(root string, check bool, generate func(string) error) error {
	return runGenerationWithRename(root, check, generate, os.Rename)
}

func runGenerationWithRename(root string, check bool, generate func(string) error, rename func(string, string) error) (err error) {
	scratchRoot := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		return fmt.Errorf("create GORM scratch directory: %w", err)
	}
	temporary, err := os.MkdirTemp(scratchRoot, "gorm-")
	if err != nil {
		return fmt.Errorf("create GORM temporary directory: %w", err)
	}
	defer func() {
		backup := filepath.Join(temporary, "previous-query")
		if _, backupErr := os.Lstat(backup); backupErr == nil {
			return
		} else if !errors.Is(backupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("inspect GORM query backup %s: %w", backup, backupErr))
			return
		}
		if cleanupErr := os.RemoveAll(temporary); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove GORM temporary directory: %w", cleanupErr))
		}
	}()

	generated := filepath.Join(temporary, generatedQueryPath)
	if err := generate(generated); err != nil {
		return fmt.Errorf("generate GORM query: %w", err)
	}
	info, err := os.Stat(generated)
	if err != nil {
		return fmt.Errorf("inspect regenerated GORM query: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("regenerated GORM query is not a directory: %s", generated)
	}

	committed := filepath.Join(root, generatedQueryPath)
	differences, err := compareDirectories(committed, generated, "")
	if err != nil {
		return fmt.Errorf("compare GORM query: %w", err)
	}
	if check {
		if len(differences) > 0 {
			return fmt.Errorf("GORM query drift detected:\n%s", strings.Join(differences, "\n"))
		}
		return nil
	}
	if err := replaceGeneratedQueryWithRename(generated, committed, temporary, rename); err != nil {
		return err
	}
	return nil
}

func generateQuery(output string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("generator panic: %v", recovered)
		}
	}()
	generator := gen.NewGenerator(gen.Config{
		OutPath:           output,
		ModelPkgPath:      "internal/store/model",
		FieldNullable:     true,
		FieldWithIndexTag: true,
		FieldWithTypeTag:  true,
	})
	generator.ApplyBasic(model.AllModels...)
	generator.Execute()
	return nil
}

func compareDirectories(expected, actual, prefix string) ([]string, error) {
	expectedNames, err := directoryEntryNames(expected)
	if err != nil {
		return nil, err
	}
	actualNames, err := directoryEntryNames(actual)
	if err != nil {
		return nil, err
	}
	for name := range actualNames {
		expectedNames[name] = struct{}{}
	}
	names := make([]string, 0, len(expectedNames))
	for name := range expectedNames {
		names = append(names, name)
	}
	sort.Strings(names)

	var differences []string
	for _, name := range names {
		relative := filepath.Join(prefix, name)
		left := filepath.Join(expected, name)
		right := filepath.Join(actual, name)
		leftInfo, leftErr := os.Stat(left)
		rightInfo, rightErr := os.Stat(right)
		if leftErr != nil && !errors.Is(leftErr, os.ErrNotExist) {
			return nil, leftErr
		}
		if rightErr != nil && !errors.Is(rightErr, os.ErrNotExist) {
			return nil, rightErr
		}
		if errors.Is(leftErr, os.ErrNotExist) || errors.Is(rightErr, os.ErrNotExist) {
			location := "regenerated query"
			if leftErr == nil {
				location = "committed query (orphan output)"
			}
			differences = append(differences, fmt.Sprintf("%s: only in %s", relative, location))
			continue
		}
		if leftInfo.IsDir() && rightInfo.IsDir() {
			nested, err := compareDirectories(left, right, relative)
			if err != nil {
				return nil, err
			}
			differences = append(differences, nested...)
			continue
		}
		if !leftInfo.Mode().IsRegular() || !rightInfo.Mode().IsRegular() {
			differences = append(differences, relative+": content differs")
			continue
		}
		leftContent, err := os.ReadFile(left)
		if err != nil {
			return nil, err
		}
		rightContent, err := os.ReadFile(right)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(leftContent, rightContent) {
			differences = append(differences, relative+": content differs")
		}
	}
	return differences, nil
}

func directoryEntryNames(directory string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]struct{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = struct{}{}
	}
	return names, nil
}

func replaceGeneratedQuery(generated, committed, scratch string) error {
	return replaceGeneratedQueryWithRename(generated, committed, scratch, os.Rename)
}

func replaceGeneratedQueryWithRename(generated, committed, scratch string, rename func(string, string) error) error {
	backup := filepath.Join(scratch, "previous-query")
	hasCommitted := true
	if _, err := os.Stat(committed); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect committed GORM query: %w", err)
		}
		hasCommitted = false
	}
	if hasCommitted {
		if err := rename(committed, backup); err != nil {
			return fmt.Errorf("back up committed GORM query from %s to %s: %w", committed, backup, err)
		}
	}
	if err := rename(generated, committed); err != nil {
		if hasCommitted {
			if restoreErr := rename(backup, committed); restoreErr != nil {
				return errors.Join(
					fmt.Errorf("install regenerated GORM query from %s to %s: %w", generated, committed, err),
					fmt.Errorf("restore committed GORM query from %s to %s: %w; backup preserved", backup, committed, restoreErr),
				)
			}
		}
		return fmt.Errorf("install regenerated GORM query from %s to %s: %w", generated, committed, err)
	}
	if hasCommitted {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove previous GORM query: %w", err)
		}
	}
	return nil
}
