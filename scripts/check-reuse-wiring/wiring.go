package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var packReportLine = regexp.MustCompile(`node scripts/build\.mjs report\b`)

func normalizedStepIf(step map[string]interface{}) string {
	return strings.ReplaceAll(str(step["if"]), " ", "")
}

func stepRun(step map[string]interface{}) string {
	return str(step["run"])
}

func stepName(step map[string]interface{}) string {
	return str(step["name"])
}

func runWiringChecks(in inputs) []checkResult {
	c := &collector{}
	check := c.check
	ci := in.ci
	release := in.release

	produced := artifactStepNames(ci, "actions/upload-artifact")
	required := resolverRequiredArtifacts(in.resolverText)
	missing := []string{}
	for name := range required {
		if !produced[name] {
			missing = append(missing, name)
		}
	}
	check("resolver required artifacts are all produced by CI", len(missing) == 0,
		fmt.Sprintf("missing from CI: %v", sortedStringSlice(missing)))

	downloads := artifactStepNames(release, "actions/download-artifact")
	downloadMissing := []string{}
	for name := range downloads {
		if !produced[name] {
			downloadMissing = append(downloadMissing, name)
		}
	}
	check("release downloads are a subset of CI producers", len(downloadMissing) == 0,
		fmt.Sprintf("downloads=%v", sortedStringSlice(downloadMissing)))

	ciSourceSteps := asList(asMap(jobsOf(release)["ci-source"])["steps"])
	checkout := false
	for _, rawStep := range ciSourceSteps {
		if strings.Contains(str(asMap(rawStep)["uses"]), "actions/checkout") {
			checkout = true
		}
	}
	check("ci-source checks out the resolver script", checkout, "")

	validateStep := findStep(asMap(jobsOf(release)["ci-source"]), "The tag drives the release version")
	validateRun := stepRun(validateStep)
	check("ci-source validates the release tag and the 0.0.0 version placeholders",
		strings.Contains(validateRun, `^v[0-9]+\.[0-9]+\.[0-9]+`) &&
			strings.Contains(validateRun, "wails.json") &&
			strings.Contains(validateRun, "lazycat/package.yml") &&
			strings.Contains(validateRun, `"0.0.0"`),
		fmt.Sprintf("run=%q", validateRun))

	scenarios := []struct {
		name     string
		scenario map[string]string
		expected map[string]string
	}{
		{"reuse: successful ci-source + skipped ci proceeds to packaging and publish",
			map[string]string{"ci-source": "success", "reused": "true", "ci": "success"},
			map[string]string{"ci-source": "success", "ci": "skipped", "desktop": "success", "server": "success", "publish": "success"}},
		{"fallback: full CI success and release proceeds",
			map[string]string{"ci-source": "success", "reused": "false", "ci": "success"},
			map[string]string{"ci-source": "success", "ci": "success", "desktop": "success", "server": "success", "publish": "success"}},
		{"fallback: CI failure gates the release",
			map[string]string{"ci-source": "success", "reused": "false", "ci": "failure"},
			map[string]string{"ci-source": "success", "ci": "failure", "desktop": "skipped", "server": "skipped", "publish": "skipped"}},
		{"reuse: desktop packaging failure still gates publish",
			map[string]string{"ci-source": "success", "reused": "true", "desktop": "failure"},
			map[string]string{"ci-source": "success", "ci": "skipped", "desktop": "failure", "server": "success", "publish": "skipped"}},
		{"reuse: server packaging failure still gates publish",
			map[string]string{"ci-source": "success", "reused": "true", "server": "failure"},
			map[string]string{"ci-source": "success", "ci": "skipped", "desktop": "success", "server": "failure", "publish": "skipped"}},
		{"ci-source failure gates the release",
			map[string]string{"ci-source": "failure", "reused": "false", "ci": "success"},
			map[string]string{"ci-source": "failure", "ci": "skipped", "desktop": "skipped", "server": "skipped", "publish": "skipped"}},
		{"ci-source cancellation gates the release",
			map[string]string{"ci-source": "cancelled", "reused": "false", "ci": "success"},
			map[string]string{"ci-source": "cancelled", "ci": "skipped", "desktop": "skipped", "server": "skipped", "publish": "skipped"}},
	}
	jobOrder := []string{"ci-source", "ci", "desktop", "server", "publish"}
	for _, s := range scenarios {
		actual := evaluateRelease(release, s.scenario)
		equal := len(actual) == len(s.expected)
		if equal {
			for key, value := range s.expected {
				if actual[key] != value {
					equal = false
					break
				}
			}
		}
		check(fmt.Sprintf("job graph: %s", s.name), equal, fmt.Sprintf("actual=%s", pyMap(actual, jobOrder)))
	}

	for _, jobKey := range []string{"desktop", "server", "publish"} {
		jobIf := strings.ReplaceAll(str(asMap(jobsOf(release)[jobKey])["if"]), " ", "")
		check(fmt.Sprintf("%s continues past a skipped reused CI without hiding failures", jobKey),
			jobIf == "${{!failure()&&!cancelled()}}",
			fmt.Sprintf("if=%q", str(asMap(jobsOf(release)[jobKey])["if"])))
	}

	qualitySteps := asList(asMap(jobsOf(ci)["quality"])["steps"])
	var typecheckOffenders []string
	var reproSteps []map[string]interface{}
	var goTestSteps []map[string]interface{}
	var productionSuiteSteps []string
	var harnessTestSteps []string
	for _, rawStep := range qualitySteps {
		step := asMap(rawStep)
		run := stepRun(step)
		if strings.Contains(run, "pnpm typecheck") {
			typecheckOffenders = append(typecheckOffenders, stepName(step))
		}
		if strings.Contains(run, "build.mjs frontend --repro-check") {
			reproSteps = append(reproSteps, step)
		}
		if strings.HasPrefix(strings.TrimSpace(run), "go test ") && !strings.Contains(run, "-tags") {
			goTestSteps = append(goTestSteps, step)
		}
		if strings.Contains(run, "-tags production") {
			productionSuiteSteps = append(productionSuiteSteps, stepName(step))
		}
		if strings.Contains(run, "node --test") {
			harnessTestSteps = append(harnessTestSteps, stepName(step))
		}
	}
	check("quality has no standalone pnpm typecheck step", len(typecheckOffenders) == 0,
		fmt.Sprintf("offenders=%v", typecheckOffenders))
	check("quality runs the reproducible frontend build that carries tsc", len(reproSteps) == 1,
		fmt.Sprintf("count=%d", len(reproSteps)))
	goSuiteOK := len(goTestSteps) == 1 &&
		!strings.Contains(stepRun(goTestSteps[0]), "-race") &&
		strings.Contains(stepRun(goTestSteps[0]), "./...")
	var goTestNames []string
	for _, step := range goTestSteps {
		goTestNames = append(goTestNames, stepName(step))
	}
	check("quality runs the Go suite once without -race", goSuiteOK,
		fmt.Sprintf("steps=%v", goTestNames))
	check("quality runs no production-tagged Go suite", len(productionSuiteSteps) == 0,
		fmt.Sprintf("steps=%v", productionSuiteSteps))
	check("quality runs no build harness unit tests", len(harnessTestSteps) == 0,
		fmt.Sprintf("steps=%v", harnessTestSteps))

	ageOffenders := minimumReleaseAgeOffenders(ci, "ci.yml")
	check("ci.yml carries no pnpm minimumReleaseAge bypass", len(ageOffenders) == 0,
		fmt.Sprintf("offenders=%v", ageOffenders))

	var packReportLines []string
	for _, line := range strings.Split(in.packText, "\n") {
		if packReportLine.MatchString(line) {
			packReportLines = append(packReportLines, line)
		}
	}
	check("pack generates the server archive evidence report exactly once",
		len(packReportLines) == 1 && strings.Contains(in.packText, "--kind=server-archive"),
		fmt.Sprintf("lines=%v", packReportLines))
	check("pack report keeps --require-evidence enforcement",
		strings.Contains(in.packText, "report_args+=(--require-evidence)") &&
			strings.Contains(in.packText, `if [ "$REQUIRE_EVIDENCE" -ne 0 ]`),
		"missing the conditional --require-evidence append")

	serverJob := asMap(jobsOf(release)["server"])
	e2eStageStep := findStep(serverJob, "Stage and verify the reused CI e2e report")
	check("server stages the reused CI e2e report only on reuse-hit",
		normalizedStepIf(e2eStageStep) == "needs.ci-source.outputs.reused=='true'",
		fmt.Sprintf("if=%q", str(e2eStageStep["if"])))
	e2eExerciseStep := findStep(serverJob, "Exercise the packaged server before archiving")
	check("server reruns e2e only without reuse",
		normalizedStepIf(e2eExerciseStep) == "needs.ci-source.outputs.reused!='true'",
		fmt.Sprintf("if=%q", str(e2eExerciseStep["if"])))

	publishStep := findStep(asMap(jobsOf(release)["publish"]), "Create or update the draft release")
	publishRun := stepRun(publishStep)
	check("release publish uploads through release-upload.mjs",
		strings.Contains(publishRun, "node .github/scripts/release-upload.mjs candidate") &&
			!strings.Contains(publishRun, "gh release upload") &&
			!strings.Contains(publishRun, "xargs"),
		fmt.Sprintf("run=%q", publishRun))
	publishEnv := asMap(publishStep["env"])
	check("release publish passes the token and repository to the uploader",
		publishEnv["GH_TOKEN"] != nil && publishEnv["GITHUB_REPOSITORY"] != nil,
		fmt.Sprintf("env=%v", sortedKeys(publishEnv)))
	assetTemplates := []string{
		"NexTerm_${version}_x64-setup.exe",
		"NexTerm_${version}_arm64-setup.exe",
		"NexTerm_${version}_aarch64.dmg",
		"NexTerm_${version}_x86_64.dmg",
		"NexTerm-desktop_${version}_linux_${arch}.deb",
		"NexTerm-server_${version}_linux_${arch}.tar.gz",
	}
	var drifted []string
	for _, template := range assetTemplates {
		if !strings.Contains(in.evidenceText, template) || !strings.Contains(in.uploadText, template) {
			drifted = append(drifted, template)
		}
	}
	check("release-upload expects the same 8 packages as the evidence gate", len(drifted) == 0,
		fmt.Sprintf("drifted=%v", drifted))
	check("release-upload expects SHA256SUMS", strings.Contains(in.uploadText, "SHA256SUMS"), "")
	for _, envName := range []string{"GITHUB_REF_NAME", "GITHUB_REPOSITORY", "GH_TOKEN", "GITHUB_API_URL"} {
		check(fmt.Sprintf("release-upload.mjs reads %s", envName), strings.Contains(in.uploadText, envName), "")
	}
	check("release-upload resolves releases through the list API, not the draft-blind tags endpoint",
		!strings.Contains(in.uploadText, "releases/tags/") && strings.Contains(in.uploadText, "releases?per_page="),
		"drafts 404 on the tags endpoint; the uploader must discover them via the paginated releases list")

	desktopSteps := asList(asMap(jobsOf(release)["desktop"])["steps"])
	cacheIndex := -1
	setupGoIndex := -1
	for i, rawStep := range desktopSteps {
		step := asMap(rawStep)
		if step["id"] == "wails-cache" && cacheIndex < 0 {
			cacheIndex = i
		}
		if strings.HasPrefix(str(step["uses"]), "actions/setup-go") && setupGoIndex < 0 {
			setupGoIndex = i
		}
	}
	check("windows wails cache restores before setup-go",
		cacheIndex >= 0 && setupGoIndex >= 0 && cacheIndex < setupGoIndex,
		fmt.Sprintf("cache_index=%d setup_go_index=%d", cacheIndex, setupGoIndex))
	setupGoCondition := ""
	if setupGoIndex >= 0 {
		setupGoCondition = str(asMap(desktopSteps[setupGoIndex])["if"])
	}
	check("desktop setup-go is never skipped by the wails cache hit",
		!strings.Contains(setupGoCondition, "wails-cache"),
		fmt.Sprintf("if=%q", setupGoCondition))

	nativeJob := asMap(jobsOf(ci)["native"])
	nativeSteps := asList(nativeJob["steps"])
	uploadStep := findStep(nativeJob, "Upload native runtime")
	var uploadPaths []string
	for _, line := range strings.Split(str(asMap(uploadStep["with"])["path"]), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			uploadPaths = append(uploadPaths, trimmed)
		}
	}
	allTarget := len(uploadPaths) > 0
	for _, path := range uploadPaths {
		if !strings.HasPrefix(path, "target/") {
			allTarget = false
		}
	}
	check("native upload keeps target/ as the ZIP root", allTarget,
		fmt.Sprintf("paths=%v", uploadPaths))
	var smokeSteps []string
	for _, rawStep := range nativeSteps {
		step := asMap(rawStep)
		if strings.Contains(stepRun(step), "--smoke") {
			smokeSteps = append(smokeSteps, stepName(step))
		}
	}
	check("native job builds and runs no smoke desktop binary", len(smokeSteps) == 0,
		fmt.Sprintf("steps=%v", smokeSteps))
	noSmokeEvidence := true
	for _, path := range uploadPaths {
		if strings.Contains(path, "desktop-smoke") {
			noSmokeEvidence = false
		}
	}
	check("native upload carries no desktop smoke evidence", noSmokeEvidence,
		fmt.Sprintf("paths=%v", uploadPaths))
	var nativeStepNames []string
	for _, rawStep := range nativeSteps {
		nativeStepNames = append(nativeStepNames, stepName(asMap(rawStep)))
	}
	containsName := func(part string) bool {
		for _, name := range nativeStepNames {
			if strings.Contains(name, part) {
				return true
			}
		}
		return false
	}
	check("native job keeps the repro-checked production build",
		containsName("Build the repro-checked production binary"), "")
	check("native job keeps the two-process server acceptance",
		containsName("Run Go two-process server acceptance"), "")

	distDownload := -1
	for i, rawStep := range nativeSteps {
		step := asMap(rawStep)
		with := asMap(step["with"])
		if with != nil &&
			strings.HasPrefix(str(step["uses"]), "actions/download-artifact") &&
			str(with["name"]) == "frontend-dist" {
			distDownload = i
			break
		}
	}
	distDownloadFound := distDownload >= 0
	distDownloadIf := ""
	distDownloadPath := ""
	if distDownloadFound {
		step := asMap(nativeSteps[distDownload])
		distDownloadIf = normalizedStepIf(step)
		distDownloadPath = str(asMap(step["with"])["path"])
	}
	check("native job downloads the verified frontend dist for every desktop build",
		distDownloadFound && distDownloadIf == "matrix.kind=='desktop'",
		fmt.Sprintf("step found=%v", distDownloadFound))
	check("native frontend dist downloads outside target/",
		distDownloadPath != "" && !strings.HasPrefix(distDownloadPath, "target/"),
		fmt.Sprintf("path=%q", distDownloadPath))
	buildIndex := -1
	for i, rawStep := range nativeSteps {
		if strings.Contains(stepName(asMap(rawStep)), "Build the repro-checked production binary") {
			buildIndex = i
			break
		}
	}
	buildStep := asMap(nativeSteps[buildIndex])
	check("native frontend dist download precedes the production build",
		distDownloadFound && distDownload < buildIndex,
		"the dist download must run before the build that consumes it")
	desktopBuildRun := matrixSubstitute(stepRun(buildStep), map[string]string{"kind": "desktop", "os": "linux", "arch": "amd64"})
	serverBuildRun := matrixSubstitute(stepRun(buildStep), map[string]string{"kind": "server", "os": "linux", "arch": "amd64"})
	check("desktop production build consumes the downloaded verified dist",
		distDownloadPath != "" &&
			strings.Contains(desktopBuildRun, "--consume-dist="+distDownloadPath+"/dist") &&
			strings.Contains(desktopBuildRun, "--dist-manifest="+distDownloadPath+"/target/dist-manifest.json"),
		fmt.Sprintf("run=%q", desktopBuildRun))
	check("desktop production build does not skip the frontend without a staged dist",
		!strings.Contains(desktopBuildRun, "--skip-frontend"),
		fmt.Sprintf("run=%q", desktopBuildRun))
	check("server production build stays free of frontend dist handling",
		!strings.Contains(serverBuildRun, "--consume-dist") && !strings.Contains(serverBuildRun, "--skip-frontend"),
		fmt.Sprintf("run=%q", serverBuildRun))
	downloadPaths := map[string]string{}
	for _, jobKey := range []string{"desktop", "server"} {
		step := findNativeDownloadStep(release, jobKey)
		path := str(asMap(step["with"])["path"])
		downloadPaths[jobKey] = path
		check(fmt.Sprintf("native download stages outside target/ (%s)", jobKey),
			path != "" && !strings.HasPrefix(path, "target/"),
			fmt.Sprintf("path=%q", path))
	}

	stagingCases := []struct {
		jobKey   string
		stepPart string
		values   [][2]string
		staged   []string
		placed   []string
	}{
		{"desktop", "Stage the reused production binary",
			[][2]string{{"os", "windows"}, {"arch", "amd64"}},
			[]string{"go-build/nexterm-desktop-windows-amd64.exe", "go-build/nexterm-desktop-windows-amd64.exe.manifest.json",
				"go-build/nexterm-desktop-windows-amd64.exe.artifact.json",
				"e2e-sync/report.json"},
			[]string{"go-build/nexterm-desktop-windows-amd64.exe", "go-build/nexterm-desktop-windows-amd64.exe.manifest.json"}},
		{"desktop", "Stage the reused production binary",
			[][2]string{{"os", "darwin"}, {"arch", "arm64"}},
			[]string{"go-build/nexterm-desktop-darwin-arm64", "go-build/nexterm-desktop-darwin-arm64.manifest.json",
				"go-build/nexterm-desktop-darwin-arm64.artifact.json"},
			[]string{"go-build/nexterm-desktop-darwin-arm64", "go-build/nexterm-desktop-darwin-arm64.manifest.json"}},
		{"server", "Stage the reused production server binary",
			[][2]string{{"arch", "amd64"}},
			[]string{"go-build/nexterm-server-linux-amd64", "go-build/nexterm-server-linux-amd64.manifest.json",
				"go-build/nexterm-server-linux-amd64.artifact.json", "e2e-sync/report.json", "e2e-sync/e2e.log"},
			[]string{"go-build/nexterm-server-linux-amd64", "go-build/nexterm-server-linux-amd64.manifest.json"}},
	}
	for _, tc := range stagingCases {
		values := map[string]string{}
		for _, pair := range tc.values {
			values[pair[0]] = pair[1]
		}
		ok, detail := exerciseStaging(release, tc.jobKey, tc.stepPart, values, tc.staged, tc.placed, downloadPaths[tc.jobKey])
		check(fmt.Sprintf("staging %s (%s)", tc.stepPart, pyDict(tc.values)), ok, detail)
	}

	binaryBytes := []byte("stub server binary bytes\n")
	binarySha := sha256.Sum256(binaryBytes)
	binaryShaHex := hex.EncodeToString(binarySha[:])
	passedReport := toJSON(map[string]interface{}{
		"schema_version": 1, "mode": "go-self-consistency", "status": "passed",
		"passed": 20, "failed": 0,
		"assertions": map[string]interface{}{"passed": []string{"a"}, "failed": []string{}},
	})
	failedReport := toJSON(map[string]interface{}{
		"schema_version": 1, "mode": "go-self-consistency", "status": "failed",
		"passed": 18, "failed": 2,
		"assertions": map[string]interface{}{"passed": []string{"a"}, "failed": []string{"b", "c"}},
	})
	e2eCases := []struct {
		name        string
		values      [][2]string
		reportText  *string
		stagedFiles []string
		manifestSha string
		expectedOK  bool
	}{
		{"e2e report staging passes a clean report bound to the binary manifest",
			[][2]string{{"arch", "amd64"}}, &passedReport, []string{"e2e-sync/e2e-a.log", "e2e-sync/e2e-b.log"}, binaryShaHex, true},
		{"e2e report staging rejects a failed report",
			[][2]string{{"arch", "amd64"}}, &failedReport, []string{"e2e-sync/e2e-a.log"}, binaryShaHex, false},
		{"e2e report staging rejects a binary that differs from the manifest",
			[][2]string{{"arch", "amd64"}}, &passedReport, []string{"e2e-sync/e2e-a.log"}, strings.Repeat("0", 64), false},
		{"e2e report staging rejects a missing report",
			[][2]string{{"arch", "amd64"}}, nil, []string{"e2e-sync/e2e-a.log"}, binaryShaHex, false},
	}
	for _, tc := range e2eCases {
		values := map[string]string{}
		for _, pair := range tc.values {
			values[pair[0]] = pair[1]
		}
		ok, detail := exerciseE2eStaging(release, values, downloadPaths["server"], tc.reportText, tc.stagedFiles, binaryBytes, tc.manifestSha)
		detailOut := ""
		if ok != tc.expectedOK {
			detailOut = fmt.Sprintf("expected_ok=%v actual_ok=%v: %s", tc.expectedOK, ok, detail)
		}
		check(tc.name, ok == tc.expectedOK, detailOut)
	}

	for _, workflow := range []struct {
		document map[string]interface{}
		label    string
	}{{ci, "ci.yml"}, {release, "release.yml"}} {
		var offenders []string
		jobs := jobsOf(workflow.document)
		for _, jobID := range sortedKeys(jobs) {
			job := asMap(jobs[jobID])
			windowsRunner := false
			for _, values := range matrixEntries(job) {
				if strings.Contains(values["runner"], "windows") {
					windowsRunner = true
				}
			}
			if !windowsRunner {
				continue
			}
			for _, rawStep := range asList(job["steps"]) {
				step := asMap(rawStep)
				script := stepRun(step)
				if script != "" && strings.Contains(script, "if [") && str(step["shell"]) != "bash" {
					offenders = append(offenders, fmt.Sprintf("%s/%s", jobID, stepName(step)))
				}
			}
		}
		check(fmt.Sprintf("%s windows bash-syntax steps declare shell: bash", workflow.label),
			len(offenders) == 0, fmt.Sprintf("offenders=%v", offenders))
	}

	return c.results
}

func sortedStringSlice(in []string) []string {
	out := append([]string(nil), in...)
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
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

func exerciseStaging(release map[string]interface{}, jobKey, stepNamePart string,
	values map[string]string, stagedFiles, expectedPlaced []string, downloadPath string) (bool, string) {
	step := findStep(asMap(jobsOf(release)[jobKey]), stepNamePart)
	script := matrixSubstitute(stepRun(step), values)
	base, err := os.MkdirTemp("", "reuse-staging-")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(base)
	for _, relative := range stagedFiles {
		target := filepath.Join(base, filepath.FromSlash(downloadPath), filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return false, err.Error()
		}
		if err := os.WriteFile(target, []byte("stub "+relative+"\n"), 0o644); err != nil {
			return false, err.Error()
		}
	}
	result := runScript(script, base, nil)
	if result.rc != 0 {
		return false, fmt.Sprintf("exit %d: %s", result.rc, result.stderr)
	}
	for _, name := range expectedPlaced {
		placed := filepath.Join(base, "target", filepath.FromSlash(name))
		if _, err := os.Stat(placed); err != nil {
			return false, fmt.Sprintf("missing: %s", placed)
		}
	}
	nesting := filepath.Join(base, "target", "go-build", "go-build")
	if _, err := os.Stat(nesting); err == nil {
		return false, fmt.Sprintf("nested %s", nesting)
	}
	evidence := filepath.Join(base, "target", "release-assets")
	if entries, err := os.ReadDir(evidence); err == nil {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return false, fmt.Sprintf("CI evidence mixed into target/release-assets: %v", names)
	}
	goos := values["os"]
	if goos == "" {
		goos = "linux"
	}
	if goos != "windows" {
		placed := filepath.Join(base, "target", filepath.FromSlash(expectedPlaced[0]))
		if info, err := os.Stat(placed); err != nil || info.Mode()&0o111 == 0 {
			return false, fmt.Sprintf("%s is not executable", placed)
		}
	}
	return true, ""
}

func exerciseE2eStaging(release map[string]interface{}, values map[string]string, downloadPath string,
	reportText *string, stagedFiles []string, binaryBytes []byte, manifestSha string) (bool, string) {
	step := findStep(asMap(jobsOf(release)["server"]), "Stage and verify the reused CI e2e report")
	script := matrixSubstitute(stepRun(step), values)
	base, err := os.MkdirTemp("", "reuse-e2e-staging-")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(base)
	binary := filepath.Join(base, "target", "go-build", "nexterm-server-linux-amd64")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		return false, err.Error()
	}
	if err := os.WriteFile(binary, binaryBytes, 0o755); err != nil {
		return false, err.Error()
	}
	manifest, _ := json.Marshal(map[string]interface{}{"artifact": map[string]interface{}{"sha256": manifestSha}})
	manifestPath := filepath.Join(base, "target", "go-build", "nexterm-server-linux-amd64.manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		return false, err.Error()
	}
	if reportText != nil {
		report := filepath.Join(base, filepath.FromSlash(downloadPath), "e2e-sync", "report.json")
		if err := os.MkdirAll(filepath.Dir(report), 0o755); err != nil {
			return false, err.Error()
		}
		if err := os.WriteFile(report, []byte(*reportText), 0o644); err != nil {
			return false, err.Error()
		}
	}
	for _, relative := range stagedFiles {
		target := filepath.Join(base, filepath.FromSlash(downloadPath), filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return false, err.Error()
		}
		if err := os.WriteFile(target, []byte("stub "+relative+"\n"), 0o644); err != nil {
			return false, err.Error()
		}
	}
	result := runScript(script, base, nil)
	if result.rc != 0 {
		return false, fmt.Sprintf("exit %d: %s", result.rc, result.stderr)
	}
	placed := []string{filepath.Join(base, "target", "e2e-sync", "report.json")}
	for _, relative := range stagedFiles {
		placed = append(placed, filepath.Join(base, "target", "e2e-sync", filepath.Base(relative)))
	}
	for _, path := range placed {
		if _, err := os.Stat(path); err != nil {
			return false, fmt.Sprintf("missing staged files: %s", path)
		}
	}
	return true, ""
}
