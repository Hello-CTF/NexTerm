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

func findStepByID(job map[string]interface{}, id string) map[string]interface{} {
	for _, rawStep := range asList(job["steps"]) {
		step := asMap(rawStep)
		if str(step["id"]) == id {
			return step
		}
	}
	panic(fmt.Sprintf("step id not found: %s", id))
}

func cacheStepMatches(step map[string]interface{}, id, condition, path, key string) bool {
	with := asMap(step["with"])
	return str(step["id"]) == id &&
		strings.HasPrefix(str(step["uses"]), "actions/cache") &&
		normalizedStepIf(step) == condition &&
		str(with["path"]) == path &&
		str(with["key"]) == key
}

func cacheMissInstallAndAlwaysVerify(script, cacheID, install, verify string) bool {
	guard := `if [ "${{ steps.` + cacheID + `.outputs.cache-hit }}" != "true" ]; then`
	guardIndex := strings.Index(script, guard)
	if guardIndex < 0 || strings.Count(script, install) != 1 || strings.Count(script, verify) != 1 {
		return false
	}
	blockEndOffset := strings.Index(script[guardIndex:], "\nfi")
	if blockEndOffset < 0 {
		return false
	}
	blockEnd := guardIndex + blockEndOffset
	installIndex := strings.Index(script, install)
	verifyIndex := strings.Index(script, verify)
	return installIndex > guardIndex && installIndex < blockEnd && verifyIndex > strings.LastIndex(script, "\nfi")
}

func runWiringChecks(in inputs) []checkResult {
	c := &collector{}
	check := c.check
	ci := in.ci
	release := in.release

	produced := artifactStepNames(ci, "actions/upload-artifact", nil)
	required := resolverRequiredArtifacts(in.resolverText)
	missing := []string{}
	for name := range required {
		if !produced[name] {
			missing = append(missing, name)
		}
	}
	check("resolver required artifacts are all produced by CI", len(missing) == 0,
		fmt.Sprintf("missing from CI: %v", sortedStringSlice(missing)))

	downloads := artifactStepNames(release, "actions/download-artifact", produced)
	downloadOutsideRequired := []string{}
	for name := range downloads {
		if !required[name] {
			downloadOutsideRequired = append(downloadOutsideRequired, name)
		}
	}
	check("release downloads are a subset of resolver required artifacts", len(downloadOutsideRequired) == 0,
		fmt.Sprintf("downloads=%v", sortedStringSlice(downloadOutsideRequired)))
	expectedAgentDownloads := []string{
		"native-linux-amd64-agent",
		"native-linux-arm64-agent",
		"native-darwin-arm64-agent",
		"native-darwin-amd64-agent",
		"native-windows-amd64-agent",
	}
	missingAgentDownloads := []string{}
	for _, name := range expectedAgentDownloads {
		if !downloads[name] {
			missingAgentDownloads = append(missingAgentDownloads, name)
		}
	}
	agentDownloadCount := 0
	for name := range downloads {
		if strings.HasPrefix(name, "native-") && strings.HasSuffix(name, "-agent") {
			agentDownloadCount++
		}
	}
	check("release agent pattern expands to all five agent artifact downloads",
		agentDownloadCount == len(expectedAgentDownloads) && len(missingAgentDownloads) == 0,
		fmt.Sprintf("count=%d missing=%v", agentDownloadCount, missingAgentDownloads))

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
			map[string]string{"ci-source": "success", "ci": "skipped", "desktop": "success", "server": "success", "agent": "success", "mobile": "success", "publish": "success"}},
		{"fallback: full CI success and release proceeds",
			map[string]string{"ci-source": "success", "reused": "false", "ci": "success"},
			map[string]string{"ci-source": "success", "ci": "success", "desktop": "success", "server": "success", "agent": "success", "mobile": "success", "publish": "success"}},
		{"fallback: CI failure gates the release",
			map[string]string{"ci-source": "success", "reused": "false", "ci": "failure"},
			map[string]string{"ci-source": "success", "ci": "failure", "desktop": "skipped", "server": "skipped", "agent": "skipped", "mobile": "skipped", "publish": "skipped"}},
		{"reuse: desktop packaging failure still gates publish",
			map[string]string{"ci-source": "success", "reused": "true", "desktop": "failure"},
			map[string]string{"ci-source": "success", "ci": "skipped", "desktop": "failure", "server": "success", "agent": "success", "mobile": "success", "publish": "skipped"}},
		{"reuse: server packaging failure still gates publish",
			map[string]string{"ci-source": "success", "reused": "true", "server": "failure"},
			map[string]string{"ci-source": "success", "ci": "skipped", "desktop": "success", "server": "failure", "agent": "success", "mobile": "success", "publish": "skipped"}},
		{"reuse: agent packaging failure still gates publish",
			map[string]string{"ci-source": "success", "reused": "true", "agent": "failure"},
			map[string]string{"ci-source": "success", "ci": "skipped", "desktop": "success", "server": "success", "agent": "failure", "mobile": "success", "publish": "skipped"}},
		{"reuse: mobile packaging failure still gates publish",
			map[string]string{"ci-source": "success", "reused": "true", "mobile": "failure"},
			map[string]string{"ci-source": "success", "ci": "skipped", "desktop": "success", "server": "success", "agent": "success", "mobile": "failure", "publish": "skipped"}},
		{"ci-source failure gates the release",
			map[string]string{"ci-source": "failure", "reused": "false", "ci": "success"},
			map[string]string{"ci-source": "failure", "ci": "skipped", "desktop": "skipped", "server": "skipped", "agent": "skipped", "mobile": "skipped", "publish": "skipped"}},
		{"ci-source cancellation gates the release",
			map[string]string{"ci-source": "cancelled", "reused": "false", "ci": "success"},
			map[string]string{"ci-source": "cancelled", "ci": "skipped", "desktop": "skipped", "server": "skipped", "agent": "skipped", "mobile": "skipped", "publish": "skipped"}},
	}
	jobOrder := []string{"ci-source", "ci", "desktop", "server", "agent", "mobile", "publish"}
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

	for _, jobKey := range []string{"desktop", "server", "agent", "mobile", "publish"} {
		jobIf := strings.ReplaceAll(str(asMap(jobsOf(release)[jobKey])["if"]), " ", "")
		check(fmt.Sprintf("%s continues past a skipped reused CI without hiding failures", jobKey),
			jobIf == "${{!failure()&&!cancelled()}}",
			fmt.Sprintf("if=%q", str(asMap(jobsOf(release)[jobKey])["if"])))
	}

	qualitySource := asMap(jobsOf(ci)["quality-source"])
	qualitySourceResolve := findStep(qualitySource, "Resolve reusable quality evidence")
	qualitySourceRun := stepRun(qualitySourceResolve)
	qualitySourceResolverCalls := 0
	for _, rawStep := range asList(qualitySource["steps"]) {
		qualitySourceResolverCalls += strings.Count(stepRun(asMap(rawStep)), "bash .github/scripts/resolve-ci-reuse.sh")
	}
	qualitySourceResolverGate := regexp.MustCompile(`(?s)if \[ "\$GITHUB_REF_TYPE" != "tag" \]; then\s+echo "reused=false" >> "\$GITHUB_OUTPUT"\s+exit 0\s+fi\s+bash \.github/scripts/resolve-ci-reuse\.sh >> "\$GITHUB_OUTPUT"`)
	check("quality-source calls the resolver only for tags and defaults non-tag reuse to false",
		qualitySourceResolverCalls == 1 && qualitySourceResolverGate.MatchString(qualitySourceRun),
		fmt.Sprintf("calls=%d run=%q", qualitySourceResolverCalls, qualitySourceRun))
	check("quality-source locks reusable quality evidence to master",
		str(asMap(qualitySourceResolve["env"])["CI_REUSE_EXPECTED_REF"]) == "master",
		fmt.Sprintf("env=%v", sortedKeys(asMap(qualitySourceResolve["env"]))))

	qualityJob := asMap(jobsOf(ci)["quality"])
	check("quality runs only when reusable master quality evidence is absent",
		strings.Join(jobNeeds(qualityJob), ",") == "version,quality-source" && normalizedStepIf(qualityJob) == "needs.quality-source.outputs.reused!='true'",
		fmt.Sprintf("needs=%v if=%q", jobNeeds(qualityJob), str(qualityJob["if"])))
	frontendQualityJob := asMap(jobsOf(ci)["frontend-quality"])
	check("frontend-quality runs only when reusable master quality evidence is absent",
		strings.Join(jobNeeds(frontendQualityJob), ",") == "version,quality-source" && normalizedStepIf(frontendQualityJob) == "needs.quality-source.outputs.reused!='true'",
		fmt.Sprintf("needs=%v if=%q", jobNeeds(frontendQualityJob), str(frontendQualityJob["if"])))
	postgresJob := asMap(jobsOf(ci)["postgres"])
	check("postgres runs only when reusable master quality evidence is absent",
		strings.Join(jobNeeds(postgresJob), ",") == "quality-source" && normalizedStepIf(postgresJob) == "needs.quality-source.outputs.reused!='true'",
		fmt.Sprintf("needs=%v if=%q", jobNeeds(postgresJob), str(postgresJob["if"])))
	for _, jobKey := range []string{"native", "mobile"} {
		job := asMap(jobsOf(ci)[jobKey])
		check(fmt.Sprintf("%s depends only on frontend and version", jobKey),
			strings.Join(jobNeeds(job), ",") == "frontend,version",
			fmt.Sprintf("needs=%v", jobNeeds(job)))
		check(fmt.Sprintf("%s keeps the complete artifact matrix off pull requests", jobKey),
			normalizedStepIf(job) == "github.event_name!='pull_request'",
			fmt.Sprintf("if=%q", str(job["if"])))
	}
	prLinuxJob := asMap(jobsOf(ci)["pr-linux-smoke"])
	prWindowsJob := asMap(jobsOf(ci)["pr-windows-smoke"])
	for _, item := range []struct {
		name string
		job  map[string]interface{}
	}{{"Linux", prLinuxJob}, {"Windows", prWindowsJob}} {
		check(fmt.Sprintf("PR %s smoke is pull-request only and depends on the verified frontend", item.name),
			strings.Join(jobNeeds(item.job), ",") == "frontend,version" && normalizedStepIf(item.job) == "github.event_name=='pull_request'",
			fmt.Sprintf("needs=%v if=%q", jobNeeds(item.job), str(item.job["if"])))
		var releaseUploads []string
		for _, rawStep := range asList(item.job["steps"]) {
			step := asMap(rawStep)
			with := asMap(step["with"])
			name := str(with["name"])
			if strings.HasPrefix(str(step["uses"]), "actions/upload-artifact") &&
				(strings.HasPrefix(name, "native-") || strings.HasPrefix(name, "mobile-")) {
				releaseUploads = append(releaseUploads, name)
			}
		}
		check(fmt.Sprintf("PR %s smoke uploads no release native or mobile artifacts", item.name),
			len(releaseUploads) == 0, fmt.Sprintf("uploads=%v", releaseUploads))
	}
	var prLinuxRuns []string
	for _, rawStep := range asList(prLinuxJob["steps"]) {
		prLinuxRuns = append(prLinuxRuns, stepRun(asMap(rawStep)))
	}
	prLinuxRun := strings.Join(prLinuxRuns, "\n")
	check("PR Linux smoke keeps desktop and server production builds plus server acceptance",
		str(prLinuxJob["runs-on"]) == "ubuntu-latest" &&
			strings.Contains(prLinuxRun, "node scripts/build.mjs desktop --release --repro-check --consume-dist=verified-dist/dist --dist-manifest=verified-dist/target/dist-manifest.json --os=linux --arch=amd64") &&
			strings.Contains(prLinuxRun, "node scripts/build.mjs server --release --repro-check --os=linux --arch=amd64") &&
			strings.Contains(prLinuxRun, "go run ./scripts/e2e-harness sync --bin target/go-build/nexterm-server-linux-amd64 --no-build"),
		"required Linux amd64 smoke command is missing")
	iosDistJob := asMap(jobsOf(ci)["ios-dist"])
	check("ios-dist keeps the reused-quality dependency graph",
		strings.Join(jobNeeds(iosDistJob), ",") == "quality-source,quality,frontend-quality,frontend,version",
		fmt.Sprintf("needs=%v", jobNeeds(iosDistJob)))
	check("ios-dist continues past skipped quality but failure and cancellation still gate it",
		normalizedStepIf(iosDistJob) == "${{!failure()&&!cancelled()&&github.event_name=='push'&&vars.NEXTERM_IOS_SIGNING=='true'}}",
		fmt.Sprintf("if=%q", str(iosDistJob["if"])))

	var frontendDistUploadJobs []string
	frontendDistUpload := map[string]interface{}{}
	for _, jobKey := range sortedKeys(jobsOf(ci)) {
		job := asMap(jobsOf(ci)[jobKey])
		for _, rawStep := range asList(job["steps"]) {
			step := asMap(rawStep)
			with := asMap(step["with"])
			if strings.HasPrefix(str(step["uses"]), "actions/upload-artifact") && str(with["name"]) == "frontend-dist" {
				frontendDistUploadJobs = append(frontendDistUploadJobs, jobKey)
				frontendDistUpload = step
			}
		}
	}
	frontendDistWith := asMap(frontendDistUpload["with"])
	frontendDistPath := str(frontendDistWith["path"])
	check("frontend job is the only frontend-dist uploader and includes dist plus its manifest",
		len(frontendDistUploadJobs) == 1 && frontendDistUploadJobs[0] == "frontend" &&
			strings.Contains(frontendDistPath, "dist/") && strings.Contains(frontendDistPath, "target/dist-manifest.json") &&
			str(frontendDistWith["if-no-files-found"]) == "error",
		fmt.Sprintf("jobs=%v path=%q", frontendDistUploadJobs, frontendDistPath))

	qualitySteps := asList(qualityJob["steps"])
	var qualityRuns []string
	for _, rawStep := range qualitySteps {
		qualityRuns = append(qualityRuns, stepRun(asMap(rawStep)))
	}
	qualityRun := strings.Join(qualityRuns, "\n")
	goTestCommand := "go test -mod=readonly -timeout=5m -p=2 -json ./..."
	exactGoTests := strings.Count(qualityRun, goTestCommand)
	check("quality runs the bounded Go suite once while allowing additional targeted suites",
		exactGoTests == 1, fmt.Sprintf("exact=%d", exactGoTests))
	goTestStep := findStep(qualityJob, "Go tests")
	goDiagnosticsStep := findStep(qualityJob, "Upload Go test diagnostics")
	goDiagnosticsWith := asMap(goDiagnosticsStep["with"])
	check("quality Go tests have bounded JSON diagnostics uploaded only after failure",
		str(goTestStep["timeout-minutes"]) == "15" &&
			strings.Contains(stepRun(goTestStep), "set -o pipefail") &&
			strings.Contains(stepRun(goTestStep), `tee "$RUNNER_TEMP/go-tests.json"`) &&
			normalizedStepIf(goDiagnosticsStep) == "${{failure()&&steps.go-tests.outcome=='failure'}}" &&
			str(goDiagnosticsWith["path"]) == "${{ runner.temp }}/go-tests.json" &&
			str(goDiagnosticsWith["if-no-files-found"]) == "error",
		fmt.Sprintf("timeout=%q if=%q", str(goTestStep["timeout-minutes"]), str(goDiagnosticsStep["if"])))
	frontendQualitySteps := asList(frontendQualityJob["steps"])
	installIndex, lintIndex, testIndex := -1, -1, -1
	installRun, lintRun, testRun := "", "", ""
	for index, rawStep := range frontendQualitySteps {
		step := asMap(rawStep)
		run := stepRun(step)
		switch stepName(step) {
		case "Install frontend dependencies":
			installIndex, installRun = index, run
		case "Frontend lint":
			lintIndex, lintRun = index, run
		case "Frontend tests":
			testIndex, testRun = index, run
		}
	}
	check("frontend-quality installs dependencies before lint and tests",
		installIndex >= 0 && lintIndex > installIndex && testIndex > installIndex && strings.TrimSpace(installRun) == "pnpm install --frozen-lockfile",
		fmt.Sprintf("install=%d lint=%d test=%d", installIndex, lintIndex, testIndex))
	check("frontend-quality keeps the full pnpm lint suite",
		strings.TrimSpace(lintRun) == "pnpm lint", fmt.Sprintf("run=%q", lintRun))
	check("frontend-quality keeps the full pnpm test suite",
		strings.TrimSpace(testRun) == "pnpm test", fmt.Sprintf("run=%q", testRun))

	ageOffenders := minimumReleaseAgeOffenders(ci, "ci.yml")
	check("ci.yml carries no pnpm minimumReleaseAge bypass", len(ageOffenders) == 0,
		fmt.Sprintf("offenders=%v", ageOffenders))

	var packReportLines []string
	for _, line := range strings.Split(in.packText, "\n") {
		if packReportLine.MatchString(line) {
			packReportLines = append(packReportLines, line)
		}
	}
	check("pack generates the server archive evidence report",
		len(packReportLines) > 0 && strings.Contains(in.packText, "--kind=server-archive"),
		fmt.Sprintf("lines=%v", packReportLines))
	check("pack report keeps --require-evidence enforcement",
		strings.Contains(in.packText, "report_args+=(--require-evidence)") &&
			strings.Contains(in.packText, `if [ "$REQUIRE_EVIDENCE" -ne 0 ]`),
		"missing the conditional --require-evidence append")

	serverJob := asMap(jobsOf(release)["server"])
	e2eStageStep := findStep(serverJob, "Stage and verify the reused CI e2e report")
	check("server stages and verifies the CI e2e report on every release path",
		normalizedStepIf(e2eStageStep) == "",
		fmt.Sprintf("if=%q", str(e2eStageStep["if"])))

	agentJob := asMap(jobsOf(release)["agent"])
	agentDownloadWith := map[string]interface{}{}
	agentDownloadFound := false
	for _, rawStep := range asList(agentJob["steps"]) {
		step := asMap(rawStep)
		with := asMap(step["with"])
		if with != nil && strings.HasPrefix(str(step["uses"]), "actions/download-artifact") && str(with["pattern"]) == "native-*-agent" {
			agentDownloadFound = true
			agentDownloadWith = with
		}
	}
	check("release agent job downloads the reused CI run agent artifacts",
		agentDownloadFound &&
			str(agentDownloadWith["run-id"]) == "${{ needs.ci-source.outputs.artifact-run-id }}" &&
			str(agentDownloadWith["merge-multiple"]) == "true",
		fmt.Sprintf("with=%v", sortedKeys(agentDownloadWith)))
	publishNeeds := jobNeeds(asMap(jobsOf(release)["publish"]))
	publishNeedsAgent := false
	for _, need := range publishNeeds {
		if need == "agent" {
			publishNeedsAgent = true
		}
	}
	check("publish collects the agent packaging job", publishNeedsAgent, fmt.Sprintf("needs=%v", publishNeeds))
	publishNeedsMobile := false
	for _, need := range publishNeeds {
		if need == "mobile" {
			publishNeedsMobile = true
		}
	}
	check("publish collects the mobile packaging job", publishNeedsMobile, fmt.Sprintf("needs=%v", publishNeeds))

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
		"NexTerm_${version}_android.apk",
		"NexTerm_${version}_ios.ipa",
		"NexTerm-desktop_${version}_linux_${arch}.deb",
		"NexTerm-server_${version}_linux_${arch}.tar.gz",
		"NexTerm-agent_${version}_linux_${arch}.tar.gz",
		"NexTerm-agent_${version}_darwin_${arch}.tar.gz",
		"NexTerm-agent_${version}_windows_amd64.zip",
	}
	var drifted []string
	for _, template := range assetTemplates {
		if !strings.Contains(in.evidenceText, template) || !strings.Contains(in.uploadText, template) {
			drifted = append(drifted, template)
		}
	}
	check("release-upload expects the same 15 packages as the evidence gate", len(drifted) == 0,
		fmt.Sprintf("drifted=%v", drifted))
	check("release-upload expects SHA256SUMS", strings.Contains(in.uploadText, "SHA256SUMS"), "")
	check("release-evidence writes SHA256SUMS for the 16-asset release", strings.Contains(in.evidenceText, "SHA256SUMS"), "")
	for _, envName := range []string{"GITHUB_REF_NAME", "GITHUB_REPOSITORY", "GH_TOKEN", "GITHUB_API_URL"} {
		check(fmt.Sprintf("release-upload.mjs reads %s", envName), strings.Contains(in.uploadText, envName), "")
	}
	desktopJob := asMap(jobsOf(release)["desktop"])
	desktopSteps := asList(desktopJob["steps"])
	setupGoIndex := -1
	for i, rawStep := range desktopSteps {
		step := asMap(rawStep)
		if strings.HasPrefix(str(step["uses"]), "actions/setup-go") && setupGoIndex < 0 {
			setupGoIndex = i
		}
	}
	setupGoCondition := ""
	if setupGoIndex >= 0 {
		setupGoStep := asMap(desktopSteps[setupGoIndex])
		setupGoCondition = str(setupGoStep["if"])
	}
	check("desktop setup-go is never skipped by the wails cache hit",
		setupGoCondition == "",
		fmt.Sprintf("if=%q", setupGoCondition))
	wailsCacheKey := "wails3-v3.0.0-beta.27-${{ runner.os }}-${{ runner.arch }}-go1.26.8"
	nsisCacheKey := "nsis-3.12-${{ runner.os }}-${{ runner.arch }}"
	check("release desktop uses exact Windows Wails and NSIS cache entries",
		cacheStepMatches(findStepByID(desktopJob, "wails-cache"), "wails-cache", "matrix.os=='windows'", "~/go/bin", wailsCacheKey) &&
			cacheStepMatches(findStepByID(desktopJob, "nsis-cache"), "nsis-cache", "matrix.os=='windows'", "C:/Program Files (x86)/NSIS", nsisCacheKey),
		"")
	releaseWailsInstall := findStep(desktopJob, "Install the pinned Wails CLI")
	releaseNSISInstall := findStep(desktopJob, "Install NSIS")
	check("release desktop installs Wails and NSIS only on cache misses and always verifies them",
		normalizedStepIf(releaseWailsInstall) == "matrix.os=='windows'" && normalizedStepIf(releaseNSISInstall) == "matrix.os=='windows'" &&
			cacheMissInstallAndAlwaysVerify(stepRun(releaseWailsInstall), "wails-cache", "go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.27", "wails3 version") &&
			cacheMissInstallAndAlwaysVerify(stepRun(releaseNSISInstall), "nsis-cache", "choco install nsis --version=3.12 --yes --no-progress", `MSYS_NO_PATHCONV=1 "/c/Program Files (x86)/NSIS/makensis.exe" /VERSION`),
		"")

	nativeJob := asMap(jobsOf(ci)["native"])
	nativeWarmCondition := "github.ref=='refs/heads/master'&&matrix.os=='windows'&&matrix.kind=='desktop'"
	nativeWailsCache := findStepByID(nativeJob, "wails-cache")
	nativeNSISCache := findStepByID(nativeJob, "nsis-cache")
	nativeToolInstall := findStep(nativeJob, "Install and verify the Windows release tools")
	check("native warm caches and tool setup pin master, Windows and desktop",
		normalizedStepIf(nativeWailsCache) == nativeWarmCondition && normalizedStepIf(nativeNSISCache) == nativeWarmCondition &&
			normalizedStepIf(nativeToolInstall) == nativeWarmCondition,
		"")
	check("native warm caches use exact Wails and NSIS keys",
		cacheStepMatches(nativeWailsCache, "wails-cache", nativeWarmCondition, "~/go/bin", wailsCacheKey) &&
			cacheStepMatches(nativeNSISCache, "nsis-cache", nativeWarmCondition, "C:/Program Files (x86)/NSIS", nsisCacheKey),
		"")
	check("native warm caches install only on misses and always verify Wails and NSIS",
		cacheMissInstallAndAlwaysVerify(stepRun(nativeToolInstall), "wails-cache", "go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.27", "wails3 version") &&
			cacheMissInstallAndAlwaysVerify(stepRun(nativeToolInstall), "nsis-cache", "choco install nsis --version=3.12 --yes --no-progress", `MSYS_NO_PATHCONV=1 "/c/Program Files (x86)/NSIS/makensis.exe" /VERSION`),
		"")
	nativeSteps := asList(nativeJob["steps"])
	powershellRuntimes := findStep(prWindowsJob, "Verify Windows PowerShell runtimes")
	realWindowsTests := findStep(prWindowsJob, "Run real Windows Go and loopback tests")
	windowsSmokeBuild := findStep(prWindowsJob, "Build the Windows desktop production smoke")
	check("PR Windows amd64 job runs real fixtures with both PowerShell runtimes and desktop production smoke",
		str(prWindowsJob["runs-on"]) == "windows-latest" &&
			str(powershellRuntimes["shell"]) == "powershell" && str(realWindowsTests["shell"]) == "powershell" &&
			strings.Contains(stepRun(powershellRuntimes), "powershell.exe") && strings.Contains(stepRun(powershellRuntimes), "pwsh.exe") &&
			strings.Contains(stepRun(realWindowsTests), "& ./.github/scripts/windows-real-tests.ps1") &&
			strings.Contains(stepRun(windowsSmokeBuild), "node scripts/build.mjs desktop --release --repro-check --consume-dist=verified-dist/dist --dist-manifest=verified-dist/target/dist-manifest.json --os=windows --arch=amd64"),
		"required Windows amd64 fixture or production smoke command is missing")
	check("real Windows targeted tests reject SKIP and require a top-level PASS",
		strings.Contains(in.windowsRealText, "go.exe test -mod=readonly -count=1 ./...") &&
			strings.Contains(in.windowsRealText, "Select-String -Path $log -Pattern '--- SKIP:'") &&
			strings.Contains(in.windowsRealText, "if ($skips.Count -ne 0)") &&
			strings.Contains(in.windowsRealText, "throw \"$Test reported SKIP; a real fixture run is required\"") &&
			strings.Contains(in.windowsRealText, "if (-not (Select-String -Path $log -Pattern $passPattern -Quiet))") &&
			strings.Contains(in.windowsRealText, "throw \"$Test did not report a top-level PASS\""),
		"the full Go suite must run and every targeted real fixture must fail on SKIP or a missing PASS")
	var missingWindowsFixtureCalls []string
	for _, call := range []string{
		"Invoke-TargetedGoTest -Package './internal/fs/local' -Test 'TestRealWindowsSecondVolume'",
		"Invoke-TargetedGoTest -Package './internal/transport/winrm' -Test 'TestRealWindowsSecondVolume'",
		"Invoke-TargetedGoTest -Package './internal/transport/ssh' -Test 'TestWindowsOpenSSHSecondVolume'",
	} {
		if !strings.Contains(in.windowsRealText, call) {
			missingWindowsFixtureCalls = append(missingWindowsFixtureCalls, call)
		}
	}
	check("real Windows fixtures cover local, WinRM and OpenSSH second-volume paths",
		len(missingWindowsFixtureCalls) == 0, fmt.Sprintf("missing=%v", missingWindowsFixtureCalls))
	winRMPathCall := "Invoke-TargetedGoTest -Package './internal/transport/winrm' -Test 'TestRealWindowsServer' -RunExpression '^TestRealWindowsServer$/^(cwd_persistence|filesystem|relative_read_write_and_backup_after_cwd_change)$' -RequiredSubtests @('cwd_persistence', 'filesystem', 'relative_read_write_and_backup_after_cwd_change')"
	check("real WinRM loopback runs the complete cwd and quoted-path subtest set",
		strings.Contains(in.windowsRealText, winRMPathCall) &&
			strings.Contains(in.windowsRealText, "$subtestPassPattern = '^    --- PASS: '") &&
			strings.Contains(in.windowsRealText, "throw \"$Test/$subtest did not report PASS\""),
		"the loopback run must include cwd persistence, filesystem quoting and relative path tests with per-subtest PASS assertions")
	check("real WinRM listeners are created with selector sets",
		strings.Contains(in.windowsRealText, "New-WSManInstance -ResourceURI winrm/config/listener -SelectorSet @{ Transport = 'HTTP'; Address = 'IP:127.0.0.1' } -ValueSet @{ Port = '5985' }") &&
			strings.Contains(in.windowsRealText, "New-WSManInstance -ResourceURI winrm/config/listener -SelectorSet $selectorSet -ValueSet $valueSet"),
		"temporary and restored listeners must separate selector properties from values")
	check("real OpenSSH fixture writes multiline configuration",
		strings.Contains(in.windowsRealText, "('Port ' + $sshPort),") &&
			strings.Contains(in.windowsRealText, "('HostKey ' + $hostKey.Replace('\\', '/')),") &&
			strings.Contains(in.windowsRealText, "('AllowUsers ' + $userName),") &&
			strings.Contains(in.windowsRealText, "('Subsystem sftp ' + $sftpServer.Replace('\\', '/')),"),
		"native PowerShell array concatenation must not collapse sshd_config into one line")
	check("real OpenSSH fixture restricts the host key ACL",
		strings.Contains(in.windowsRealText, "& icacls.exe $hostKey /inheritance:r /remove:g ('*' + $currentUserSid) /grant '*S-1-5-18:F' /grant '*S-1-5-32-544:F' /Q") &&
			strings.Contains(in.windowsRealText, "& icacls.exe $hostKey /setowner '*S-1-5-18' /Q"),
		"the service host key must be owned by SYSTEM and inaccessible to unrelated users")
	check("real OpenSSH fixture runs under a LocalSystem service",
		strings.Contains(in.windowsRealText, "$null = New-Service -Name $serviceName -BinaryPathName $binaryPath -StartupType Manual") &&
			strings.Contains(in.windowsRealText, "Start-Service $sshServiceName"),
		"an elevated portable sshd cannot create user processes; the fixture must use the service control manager")
	check("real Windows Go tests select Git Bash and System32 whoami",
		strings.Contains(in.windowsRealText, "$env:PATH = $gitBin + ';' + $system32 + ';' + $env:PATH") &&
			strings.Contains(in.windowsRealText, "Git Bash must precede any WSL bash on PATH") &&
			strings.Contains(in.windowsRealText, "Windows System32 whoami must precede Unix whoami on PATH"),
		"the PATH must reject WSL bash and Unix whoami before running the Go suite")
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
			windowsRunner := strings.Contains(str(job["runs-on"]), "windows")
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
