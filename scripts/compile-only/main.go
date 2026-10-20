package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type listedPackage struct {
	Dir          string
	ImportPath   string
	Name         string
	TestGoFiles  []string
	XTestGoFiles []string
}

type runOptions struct {
	dir      string
	env      []string
	stdout   io.Writer
	stderr   io.Writer
	tempRoot string
}

type compileConfig struct {
	dir         string
	env         []string
	stdout      io.Writer
	stderr      io.Writer
	tempRoot    string
	parallelism int
	patterns    []string
}

func main() {
	err := run(context.Background(), os.Args[1:], runOptions{
		dir:    ".",
		env:    os.Environ(),
		stdout: os.Stdout,
		stderr: os.Stderr,
	})
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "compile-only: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, options runOptions) error {
	if options.dir == "" {
		options.dir = "."
	}
	if options.env == nil {
		options.env = os.Environ()
	}
	if options.stdout == nil {
		options.stdout = io.Discard
	}
	if options.stderr == nil {
		options.stderr = io.Discard
	}

	flags := flag.NewFlagSet("compile-only", flag.ContinueOnError)
	flags.SetOutput(options.stderr)
	parallelism := flags.Int("p", 0, "number of Go build commands to run in parallel (default inherits Go behavior)")
	flags.Usage = func() {
		fmt.Fprintln(options.stderr, "Usage: go run ./scripts/compile-only [-p=N] [package patterns]")
		fmt.Fprintln(options.stderr, "Compile test binaries without running TestMain or tests, and compile-check packages without tests.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *parallelism < 0 {
		return fmt.Errorf("-p must be positive, got %d", *parallelism)
	}
	patterns := flags.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "-") {
			return fmt.Errorf("package pattern %q starts with a flag prefix; use -- before package patterns", pattern)
		}
	}

	return compile(ctx, compileConfig{
		dir:         options.dir,
		env:         options.env,
		stdout:      options.stdout,
		stderr:      options.stderr,
		tempRoot:    options.tempRoot,
		parallelism: *parallelism,
		patterns:    patterns,
	})
}

func compile(ctx context.Context, config compileConfig) (resultErr error) {
	packages, err := listPackages(ctx, config)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return errors.New("go list matched no packages")
	}
	targetOS, err := targetGOOS(ctx, config)
	if err != nil {
		return err
	}
	outputDir, err := os.MkdirTemp(config.tempRoot, "nexterm-compile-only-*")
	if err != nil {
		return fmt.Errorf("create temporary output directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(outputDir); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary output directory %s: %w", outputDir, err))
		}
	}()

	testPackages := 0
	nonTestPackages := 0
	for index, pkg := range packages {
		hasTests := len(pkg.TestGoFiles)+len(pkg.XTestGoFiles) > 0
		operation := "check"
		if hasTests {
			operation = "test"
		}
		args := []string{"test", "-c", "-mod=readonly"}
		if config.parallelism > 0 {
			args = append(args, "-p="+strconv.Itoa(config.parallelism))
		}
		output := artifactPath(outputDir, index, pkg, hasTests, targetOS)
		args = append(args, "-o", output, pkg.ImportPath)
		fmt.Fprintf(config.stderr, "compile-only: %s %s\n", operation, pkg.ImportPath)
		if err := runGo(ctx, config, args...); err != nil {
			return fmt.Errorf("%s: go test -c failed: %w", pkg.ImportPath, err)
		}
		if hasTests {
			info, err := os.Stat(output)
			if err != nil {
				return fmt.Errorf("%s: go test -c succeeded but its output is unavailable: %w", pkg.ImportPath, err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s: go test -c output %s is not a regular file", pkg.ImportPath, output)
			}
			testPackages++
		} else {
			nonTestPackages++
		}
	}
	fmt.Fprintf(config.stdout, "compile-only: success: test packages compiled=%d, non-test packages checked=%d, test binaries executed=0\n", testPackages, nonTestPackages)
	return nil
}

func listPackages(ctx context.Context, config compileConfig) ([]listedPackage, error) {
	args := []string{"list", "-json", "-mod=readonly"}
	if config.parallelism > 0 {
		args = append(args, "-p="+strconv.Itoa(config.parallelism))
	}
	args = append(args, config.patterns...)
	var stdout bytes.Buffer
	if err := runGoWithOutput(ctx, config, &stdout, config.stderr, args...); err != nil {
		return nil, fmt.Errorf("discover packages: %w", err)
	}
	decoder := json.NewDecoder(&stdout)
	var packages []listedPackage
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if pkg.ImportPath == "" {
			return nil, errors.New("go list returned a package without an import path")
		}
		packages = append(packages, pkg)
	}
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].ImportPath == packages[j].ImportPath {
			return packages[i].Dir < packages[j].Dir
		}
		return packages[i].ImportPath < packages[j].ImportPath
	})
	return packages, nil
}

func targetGOOS(ctx context.Context, config compileConfig) (string, error) {
	var stdout bytes.Buffer
	if err := runGoWithOutput(ctx, config, &stdout, config.stderr, "env", "GOOS"); err != nil {
		return "", fmt.Errorf("determine target GOOS: %w", err)
	}
	target := strings.TrimSpace(stdout.String())
	if target == "" {
		return "", errors.New("go env GOOS returned an empty value")
	}
	return target, nil
}

func artifactPath(outputDir string, index int, pkg listedPackage, test bool, targetOS string) string {
	digest := sha256.Sum256([]byte(pkg.ImportPath + "\x00" + pkg.Dir))
	kind := "build"
	if test {
		kind = "test"
	}
	name := fmt.Sprintf("%04d-%x.%s", index, digest[:8], kind)
	if targetOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(outputDir, name)
}

func runGo(ctx context.Context, config compileConfig, args ...string) error {
	return runGoWithOutput(ctx, config, config.stdout, config.stderr, args...)
}

func runGoWithOutput(ctx context.Context, config compileConfig, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = config.dir
	cmd.Env = config.env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
