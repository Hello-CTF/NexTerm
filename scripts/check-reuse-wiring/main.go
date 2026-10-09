package main

import (
	"fmt"
	"os"
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

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	matrixExprRe  = regexp.MustCompile(`\$\{\{(.*?)\}\}`)
	matrixSimple  = regexp.MustCompile(`^matrix\.(\w+)$`)
	matrixTernary = regexp.MustCompile(`^matrix\.(\w+) == '([\w-]+)' && '([^']*)' \|\| '([^']*)'$`)
)

func matrixSubstitute(script string, values map[string]string) string {
	return matrixExprRe.ReplaceAllStringFunc(script, func(match string) string {
		expr := strings.TrimSpace(match[3 : len(match)-2])
		if simple := matrixSimple.FindStringSubmatch(expr); simple != nil {
			value, ok := values[simple[1]]
			if !ok {
				panic(fmt.Sprintf("matrix value missing: %s", simple[1]))
			}
			return value
		}
		if ternary := matrixTernary.FindStringSubmatch(expr); ternary != nil {
			if values[ternary[1]] == ternary[2] {
				return ternary[3]
			}
			return ternary[4]
		}
		panic(fmt.Sprintf("unhandled matrix expression: %s", expr))
	})
}

func matrixEntries(job map[string]interface{}) []map[string]string {
	strategy := asMap(job["strategy"])
	if strategy == nil {
		return []map[string]string{{}}
	}
	matrix := asMap(strategy["matrix"])
	if matrix == nil {
		return []map[string]string{{}}
	}
	include := asList(matrix["include"])
	if len(include) == 0 {
		return []map[string]string{{}}
	}
	entries := make([]map[string]string, 0, len(include))
	for _, item := range include {
		entry := map[string]string{}
		for k, v := range asMap(item) {
			entry[k] = str(v)
		}
		entries = append(entries, entry)
	}
	return entries
}

func artifactStepNames(workflow map[string]interface{}, actionPrefix string) map[string]bool {
	names := map[string]bool{}
	jobs := jobsOf(workflow)
	for _, jobKey := range sortedKeys(jobs) {
		job := asMap(jobs[jobKey])
		for _, values := range matrixEntries(job) {
			for _, rawStep := range asList(job["steps"]) {
				step := asMap(rawStep)
				with := asMap(step["with"])
				if with == nil {
					continue
				}
				if strings.HasPrefix(str(step["uses"]), actionPrefix) && str(with["name"]) != "" {
					names[matrixSubstitute(str(with["name"]), values)] = true
				}
			}
		}
	}
	return names
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

func findNativeDownloadStep(release map[string]interface{}, jobKey string) map[string]interface{} {
	job := asMap(jobsOf(release)[jobKey])
	for _, rawStep := range asList(job["steps"]) {
		step := asMap(rawStep)
		with := asMap(step["with"])
		if with == nil {
			continue
		}
		if strings.HasPrefix(str(step["uses"]), "actions/download-artifact") &&
			strings.HasPrefix(str(with["name"]), "native-") {
			return step
		}
	}
	panic(fmt.Sprintf("native download step not found in %s", jobKey))
}

func jobNeeds(job map[string]interface{}) []string {
	switch needs := job["needs"].(type) {
	case string:
		return []string{needs}
	case []interface{}:
		out := make([]string, 0, len(needs))
		for _, n := range needs {
			out = append(out, str(n))
		}
		return out
	}
	return nil
}

func minimumReleaseAgeOffenders(node interface{}, path string) []string {
	var offenders []string
	containsBypass := func(s string) bool {
		lower := strings.ToLower(s)
		return strings.Contains(lower, "minimumreleaseage") || strings.Contains(lower, "minimum_release_age")
	}
	switch v := node.(type) {
	case map[string]interface{}:
		for _, key := range sortedKeys(v) {
			if containsBypass(key) {
				offenders = append(offenders, fmt.Sprintf("%s key %s", path, key))
			}
			offenders = append(offenders, minimumReleaseAgeOffenders(v[key], path+"."+key)...)
		}
	case []interface{}:
		for i, item := range v {
			offenders = append(offenders, minimumReleaseAgeOffenders(item, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case string:
		if containsBypass(v) {
			offenders = append(offenders, path)
		}
	}
	return offenders
}

func evaluateRelease(release map[string]interface{}, scenario map[string]string) map[string]string {
	results := map[string]string{"ci-source": scenario["ci-source"]}
	outputs := map[string]string{}
	if scenario["ci-source"] == "success" {
		outputs["reused"] = scenario["reused"]
	}
	order := []string{"ci", "desktop", "server", "publish"}
	jobs := jobsOf(release)
	ancestors := map[string]map[string]bool{"ci-source": {}}
	for _, jobID := range order {
		closure := map[string]bool{}
		for _, need := range jobNeeds(asMap(jobs[jobID])) {
			closure[need] = true
			for ancestor := range ancestors[need] {
				closure[ancestor] = true
			}
		}
		ancestors[jobID] = closure
	}
	statusFunctions := []string{"success(", "failure(", "cancelled(", "always(", "skipped("}
	for _, jobID := range order {
		job := asMap(jobs[jobID])
		var expr *string
		if raw, present := job["if"]; present && raw != nil {
			normalized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(str(raw), "${{", ""), "}}", ""))
			expr = &normalized
		}
		var upstream []string
		for ancestor := range ancestors[jobID] {
			upstream = append(upstream, results[ancestor])
		}
		hasStatusCheck := false
		if expr != nil {
			for _, fn := range statusFunctions {
				if strings.Contains(*expr, fn) {
					hasStatusCheck = true
					break
				}
			}
		}
		var runs bool
		if hasStatusCheck {
			if *expr != "!failure() && !cancelled()" {
				panic(fmt.Sprintf("unhandled job-level if: %s", *expr))
			}
			runs = true
			for _, result := range upstream {
				if result == "failure" || result == "cancelled" {
					runs = false
				}
			}
		} else {
			runs = true
			for _, result := range upstream {
				if result != "success" {
					runs = false
				}
			}
			if expr != nil {
				if *expr != "needs.ci-source.outputs.reused != 'true'" {
					panic(fmt.Sprintf("unhandled job-level if: %s", *expr))
				}
				runs = runs && outputs["reused"] != "true"
			}
		}
		if runs {
			result, present := scenario[jobID]
			if !present {
				result = "success"
			}
			results[jobID] = result
		} else {
			results[jobID] = "skipped"
		}
	}
	return results
}

func pyDict(pairs [][2]string) string {
	parts := make([]string, len(pairs))
	for i, pair := range pairs {
		parts[i] = fmt.Sprintf("'%s': '%s'", pair[0], pair[1])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func pyMap(m map[string]string, order []string) string {
	parts := make([]string, 0, len(m))
	for _, key := range order {
		if value, present := m[key]; present {
			parts = append(parts, fmt.Sprintf("'%s': '%s'", key, value))
		}
	}
	for _, key := range sortedSetKeys(m) {
		present := false
		for _, ordered := range order {
			if ordered == key {
				present = true
				break
			}
		}
		if !present {
			parts = append(parts, fmt.Sprintf("'%s': '%s'", key, m[key]))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func sortedSetKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type inputs struct {
	ci           map[string]interface{}
	release      map[string]interface{}
	resolverText string
	packText     string
	uploadText   string
	evidenceText string
	root         string
}

func main() {
	started := time.Now()
	root := repoRoot()
	ciRaw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		fatalf("%v", err)
	}
	releaseRaw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		fatalf("%v", err)
	}
	resolverText, err := os.ReadFile(filepath.Join(root, ".github", "scripts", "resolve-ci-reuse.sh"))
	if err != nil {
		fatalf("%v", err)
	}
	packText, err := os.ReadFile(filepath.Join(root, "scripts", "pack-linux-server.sh"))
	if err != nil {
		fatalf("%v", err)
	}
	uploadText, err := os.ReadFile(filepath.Join(root, ".github", "scripts", "release-upload.mjs"))
	if err != nil {
		fatalf("%v", err)
	}
	evidenceText, err := os.ReadFile(filepath.Join(root, ".github", "scripts", "release-evidence.mjs"))
	if err != nil {
		fatalf("%v", err)
	}

	in := inputs{
		ci:           parseYAML(ciRaw),
		release:      parseYAML(releaseRaw),
		resolverText: string(resolverText),
		packText:     string(packText),
		uploadText:   string(uploadText),
		evidenceText: string(evidenceText),
		root:         root,
	}

	wiringStarted := time.Now()
	wiring := runWiringChecks(in)
	wiringElapsed := time.Since(wiringStarted)

	resolverStarted := time.Now()
	resolver := runResolverChecks(in.resolverText)
	resolverElapsed := time.Since(resolverStarted)

	evidenceStarted := time.Now()
	evidence := runEvidenceChecks(in)
	evidenceElapsed := time.Since(evidenceStarted)

	uploadStarted := time.Now()
	upload := runUploadChecks(root)
	uploadElapsed := time.Since(uploadStarted)

	results := append(append(append(append([]checkResult{}, wiring...), resolver...), upload...), evidence...)
	var failures []string
	for _, result := range results {
		if result.ok {
			fmt.Printf("ok - %s\n", result.name)
		} else {
			fmt.Printf("FAIL - %s: %s\n", result.name, result.detail)
			failures = append(failures, fmt.Sprintf("%s: %s", result.name, result.detail))
		}
	}

	controlsStarted := time.Now()
	missed := runNegativeControls(ciRaw, releaseRaw, in)
	failures = append(failures, missed...)
	controlsElapsed := time.Since(controlsStarted)

	if len(failures) > 0 {
		fmt.Printf("\n%d check(s) failed\n", len(failures))
		os.Exit(1)
	}
	fmt.Printf("\nall %d workflow reuse wiring checks passed\n", len(results)+len(negativeControls))
	fmt.Printf("sections: wiring %d checks %.1fs; resolver %d scenarios %.1fs; upload stub %d checks %.1fs; "+
		"desktop evidence %d checks %.1fs; %d negative controls (wiring-only re-runs) %.1fs; total %.1fs\n",
		len(wiring), wiringElapsed.Seconds(),
		len(resolver), resolverElapsed.Seconds(),
		len(upload), uploadElapsed.Seconds(),
		len(evidence), evidenceElapsed.Seconds(),
		len(negativeControls), controlsElapsed.Seconds(),
		time.Since(started).Seconds())
}
