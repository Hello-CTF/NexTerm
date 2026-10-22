package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type checkResult struct {
	name   string
	ok     bool
	detail string
}

type collector struct {
	results []checkResult
}

func (c *collector) check(name string, ok bool, detail string) {
	c.results = append(c.results, checkResult{name: name, ok: ok, detail: detail})
}

func repoRoot() string {
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		return filepath.Dir(filepath.Dir(filepath.Dir(file)))
	}
	return "."
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type cmdResult struct {
	rc     int
	stdout string
	stderr string
}

func runScript(script, dir string, env []string) cmdResult {
	cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	return runCmd(cmd)
}

func runCmd(cmd *exec.Cmd) cmdResult {
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	rc := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		rc = exitErr.ExitCode()
	} else if err != nil {
		rc = -1
		stderr.WriteString(err.Error())
	}
	return cmdResult{rc: rc, stdout: stdout.String(), stderr: stderr.String()}
}

func releaseEvidenceScriptCheck(root string) checkResult {
	cmd := exec.Command("node", "--test", filepath.Join(root, ".github", "scripts", "release-evidence.test.mjs"))
	cmd.Dir = root
	result := runCmd(cmd)
	return checkResult{
		name:   "release candidate evidence contract tests",
		ok:     result.rc == 0,
		detail: tail(result.stdout+result.stderr, 2000),
	}
}

func main() {
	root := repoRoot()
	results := runEvidenceChecks(root)
	results = append(results, releaseEvidenceScriptCheck(root))
	failed := false
	for _, result := range results {
		if result.ok {
			fmt.Printf("ok - %s\n", result.name)
		} else {
			fmt.Printf("FAIL - %s: %s\n", result.name, result.detail)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}
