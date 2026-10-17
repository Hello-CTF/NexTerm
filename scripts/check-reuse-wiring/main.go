package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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

func parseYAML(data []byte) map[string]interface{} {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		fatalf("%v", err)
	}
	return doc
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func asList(v interface{}) []interface{} {
	l, _ := v.([]interface{})
	return l
}

func str(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func jobsOf(workflow map[string]interface{}) map[string]interface{} {
	return asMap(workflow["jobs"])
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSetKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringSlice(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func resolverRequiredArtifacts(resolverText string) map[string]bool {
	match := regexp.MustCompile(`required=\(([^)]*)\)`).FindStringSubmatch(resolverText)
	if match == nil {
		panic("resolve-ci-reuse.sh has no required=(...) list")
	}
	required := map[string]bool{}
	for _, name := range strings.Fields(match[1]) {
		required[name] = true
	}
	return required
}

func findStep(job map[string]interface{}, namePart string) map[string]interface{} {
	for _, rawStep := range asList(job["steps"]) {
		step := asMap(rawStep)
		if strings.Contains(str(step["name"]), namePart) {
			return step
		}
	}
	panic(fmt.Sprintf("step not found: %s", namePart))
}

func stepRun(step map[string]interface{}) string {
	return str(step["run"])
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
	var stdout, stderr bytes.Buffer
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

type inputs struct {
	release      map[string]interface{}
	resolverText string
	root         string
}

func main() {
	started := time.Now()
	root := repoRoot()
	releaseRaw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		fatalf("%v", err)
	}
	resolverText, err := os.ReadFile(filepath.Join(root, ".github", "scripts", "resolve-ci-reuse.sh"))
	if err != nil {
		fatalf("%v", err)
	}

	in := inputs{
		release:      parseYAML(releaseRaw),
		resolverText: string(resolverText),
		root:         root,
	}

	resolverStarted := time.Now()
	resolver := runResolverChecks(in.resolverText)
	resolverElapsed := time.Since(resolverStarted)

	fallbackStarted := time.Now()
	fallback := runFallbackStagingChecks(in)
	fallbackElapsed := time.Since(fallbackStarted)

	evidenceStarted := time.Now()
	evidence := runEvidenceChecks(in)
	evidenceElapsed := time.Since(evidenceStarted)

	uploadStarted := time.Now()
	upload := runUploadChecks(root)
	uploadElapsed := time.Since(uploadStarted)

	notesStarted := time.Now()
	notes := runNotesChecks(root)
	notesElapsed := time.Since(notesStarted)

	results := append(append(append(append(append([]checkResult{}, resolver...), fallback...), upload...), notes...), evidence...)
	var failures []string
	for _, result := range results {
		if result.ok {
			fmt.Printf("ok - %s\n", result.name)
		} else {
			fmt.Printf("FAIL - %s: %s\n", result.name, result.detail)
			failures = append(failures, fmt.Sprintf("%s: %s", result.name, result.detail))
		}
	}
	if len(failures) > 0 {
		fmt.Printf("\n%d check(s) failed\n", len(failures))
		os.Exit(1)
	}
	fmt.Printf("\nall %d release behavior checks passed\n", len(results))
	fmt.Printf("sections: resolver %d scenarios %.1fs; fallback staging %d checks %.1fs; upload stub %d checks %.1fs; release notes %d checks %.1fs; desktop evidence %d checks %.1fs; total %.1fs\n",
		len(resolver), resolverElapsed.Seconds(),
		len(fallback), fallbackElapsed.Seconds(),
		len(upload), uploadElapsed.Seconds(),
		len(notes), notesElapsed.Seconds(),
		len(evidence), evidenceElapsed.Seconds(),
		time.Since(started).Seconds())
}
