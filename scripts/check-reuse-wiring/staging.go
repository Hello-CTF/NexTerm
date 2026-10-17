package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func outputValue(output, key string) string {
	for _, line := range strings.Split(output, "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok && name == key {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func releaseStagingScript(root, base, arch, downloadPath, script string) string {
	script = strings.ReplaceAll(script, "${{ matrix.arch }}", arch)
	script = strings.ReplaceAll(script, "node scripts/", "node "+shellQuote(filepath.Join(root, "scripts"))+"/")
	script = strings.ReplaceAll(script, "node .github/", "node "+shellQuote(filepath.Join(root, ".github"))+"/")
	script = strings.ReplaceAll(script, "reused-native", downloadPath)
	script = strings.ReplaceAll(script, "target/", filepath.Join(base, "target")+"/")
	return script
}

func runFallbackStagingChecks(in inputs) []checkResult {
	c := &collector{}
	name := "release fallback reuse=false stages current-run server binary and clean e2e evidence"
	ciJob := asMap(jobsOf(in.release)["ci"])
	condition := strings.TrimSpace(str(ciJob["if"]))
	condition = strings.NewReplacer("${{", "", "}}", "").Replace(condition)
	condition = strings.ReplaceAll(condition, "needs.ci-source.outputs.reused", `"false"`)
	conditionResult := runScript("if [ "+condition+" ]; then exit 0; else exit 1; fi", in.root, nil)
	if conditionResult.rc != 0 {
		c.check(name, false, fmt.Sprintf("fallback ci condition did not run for reuse=false: %q", str(ciJob["if"])))
		return c.results
	}

	resolved := runResolver(in.resolverText, []string{runsReply()}, nil, nil)
	if resolved.rc != 0 || outputValue(resolved.stdout, "reused") != "false" {
		c.check(name, false, fmt.Sprintf("resolver did not select fallback: rc=%d stdout=%q stderr=%q", resolved.rc, resolved.stdout, resolved.stderr))
		return c.results
	}
	artifactRunID := outputValue(resolved.stdout, "artifact-run-id")
	serverJob := asMap(jobsOf(in.release)["server"])
	downloadStep := findStep(serverJob, "Download the CI production server binary")
	downloadWith := asMap(downloadStep["with"])
	runIDExpression := strings.TrimSpace(str(downloadWith["run-id"]))
	runIDExpression = strings.TrimSpace(strings.NewReplacer("${{", "", "}}", "").Replace(runIDExpression))
	runID, ok := map[string]string{"needs.ci-source.outputs.artifact-run-id": artifactRunID}[runIDExpression]
	if !ok || runID == "" {
		c.check(name, false, fmt.Sprintf("server download cannot resolve the current run id: %q", runIDExpression))
		return c.results
	}
	downloadPath := str(downloadWith["path"])
	if downloadPath == "" {
		c.check(name, false, "server download has no staging path")
		return c.results
	}

	base, err := os.MkdirTemp("", "release-fallback-")
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(base)
	buildDir := filepath.Join(base, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		fatalf("%v", err)
	}
	built, err := buildStubBinary(buildDir, "package main\n\nfunc main() {}\n", "linux", "amd64", "0")
	if err != nil {
		fatalf("%v", err)
	}
	artifactRoot := filepath.Join(base, downloadPath)
	sourceBinary := filepath.Join(artifactRoot, "go-build", "nexterm-server-linux-amd64")
	if err := os.MkdirAll(filepath.Dir(sourceBinary), 0o755); err != nil {
		fatalf("%v", err)
	}
	binaryBytes, err := os.ReadFile(built)
	if err != nil {
		fatalf("%v", err)
	}
	if err := os.WriteFile(sourceBinary, binaryBytes, 0o755); err != nil {
		fatalf("%v", err)
	}
	head := runScript("git rev-parse HEAD", in.root, nil)
	if head.rc != 0 {
		c.check(name, false, fmt.Sprintf("cannot resolve fixture commit: %q", head.stderr))
		return c.results
	}
	version := "1.2.3"
	if err := writeBinaryManifest(sourceBinary, "server", "linux", "amd64", "0", version, strings.TrimSpace(head.stdout)); err != nil {
		fatalf("%v", err)
	}
	e2eDir := filepath.Join(artifactRoot, "e2e-sync")
	if err := os.MkdirAll(e2eDir, 0o755); err != nil {
		fatalf("%v", err)
	}
	report := []byte("{\"schema_version\":1,\"status\":\"passed\",\"passed\":20,\"failed\":0}\n")
	if err := os.WriteFile(filepath.Join(e2eDir, "report.json"), report, 0o644); err != nil {
		fatalf("%v", err)
	}
	if err := os.WriteFile(filepath.Join(e2eDir, "e2e.log"), []byte("two-process acceptance passed\n"), 0o644); err != nil {
		fatalf("%v", err)
	}

	steps := []string{
		"Stage the reused production server binary",
		"Verify the reused production server binary against its manifest",
		"Re-assert the reused binary evidence",
		"Stage and verify the reused CI e2e report",
	}
	env := append(os.Environ(), "NEXTERM_RELEASE_VERSION="+version, "SOURCE_DATE_EPOCH=1")
	for _, stepName := range steps {
		step := findStep(serverJob, stepName)
		result := runScript(releaseStagingScript(in.root, base, "amd64", downloadPath, stepRun(step)), base, env)
		if result.rc != 0 {
			c.check(name, false, fmt.Sprintf("%s failed for run %s: rc=%d stdout=%q stderr=%q", stepName, runID, result.rc, result.stdout, result.stderr))
			return c.results
		}
	}
	stagedReport, err := os.ReadFile(filepath.Join(base, "target", "e2e-sync", "report.json"))
	if err != nil || string(stagedReport) != string(report) {
		c.check(name, false, fmt.Sprintf("staged e2e report differs from the clean CI report: %v", err))
		return c.results
	}
	_, assertions, ok := readArtifactReport(filepath.Join(base, "target", "go-build", "nexterm-server-linux-amd64.artifact.json"))
	if !ok || assertions["binary-format"] != "passed" || assertions["binary-architecture"] != "passed" {
		c.check(name, false, fmt.Sprintf("staged server binary evidence did not pass: %v", assertions))
		return c.results
	}
	c.check(name, true, "")

	exerciseE2E := func(caseName, casePath string, caseReport []byte, mismatch bool) (cmdResult, bool) {
		caseBase := filepath.Join(base, caseName)
		artifactRoot := filepath.Join(caseBase, casePath)
		sourceBinary := filepath.Join(artifactRoot, "go-build", "nexterm-server-linux-amd64")
		if err := os.MkdirAll(filepath.Dir(sourceBinary), 0o755); err != nil {
			fatalf("%v", err)
		}
		if err := os.WriteFile(sourceBinary, binaryBytes, 0o755); err != nil {
			fatalf("%v", err)
		}
		commit := strings.TrimSpace(head.stdout)
		if err := writeBinaryManifest(sourceBinary, "server", "linux", "amd64", "0", version, commit); err != nil {
			fatalf("%v", err)
		}
		if mismatch {
			manifestPath := sourceBinary + ".manifest.json"
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				fatalf("%v", err)
			}
			var manifest map[string]interface{}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				fatalf("%v", err)
			}
			asMap(manifest["artifact"])["sha256"] = strings.Repeat("0", 64)
			raw, err = json.Marshal(manifest)
			if err != nil {
				fatalf("%v", err)
			}
			if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
				fatalf("%v", err)
			}
		}
		if caseReport != nil {
			e2eDir := filepath.Join(artifactRoot, "e2e-sync")
			if err := os.MkdirAll(e2eDir, 0o755); err != nil {
				fatalf("%v", err)
			}
			if err := os.WriteFile(filepath.Join(e2eDir, "report.json"), caseReport, 0o644); err != nil {
				fatalf("%v", err)
			}
		}
		stageStep := findStep(serverJob, "Stage the reused production server binary")
		staged := runScript(releaseStagingScript(in.root, caseBase, "amd64", casePath, stepRun(stageStep)), caseBase, env)
		if staged.rc != 0 {
			return staged, false
		}
		e2eStep := findStep(serverJob, "Stage and verify the reused CI e2e report")
		return runScript(releaseStagingScript(in.root, caseBase, "amd64", casePath, stepRun(e2eStep)), caseBase, env), true
	}

	failedReport := []byte("{\"schema_version\":1,\"status\":\"failed\",\"passed\":18,\"failed\":2}\n")
	failed, failedRan := exerciseE2E("failed-report", downloadPath, failedReport, false)
	c.check("release fallback rejects a failed e2e report", failedRan && failed.rc != 0,
		fmt.Sprintf("reached_e2e=%v rc=%d stderr=%q", failedRan, failed.rc, failed.stderr))

	mismatched, mismatchedRan := exerciseE2E("manifest-mismatch", downloadPath, report, true)
	c.check("release fallback rejects a manifest SHA256 mismatch", mismatchedRan && mismatched.rc != 0,
		fmt.Sprintf("reached_e2e=%v rc=%d stderr=%q", mismatchedRan, mismatched.rc, mismatched.stderr))

	missing, missingRan := exerciseE2E("missing-report", downloadPath, nil, false)
	c.check("release fallback rejects a missing e2e report", missingRan && missing.rc != 0,
		fmt.Sprintf("reached_e2e=%v rc=%d stderr=%q", missingRan, missing.rc, missing.stderr))

	nested, nestedRan := exerciseE2E("target-download", "target", report, false)
	c.check("release staging rejects a target download path", !nestedRan && nested.rc != 0,
		fmt.Sprintf("reached_e2e=%v rc=%d stderr=%q", nestedRan, nested.rc, nested.stderr))
	return c.results
}
