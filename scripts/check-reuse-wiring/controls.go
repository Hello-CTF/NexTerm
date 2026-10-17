package main

import (
	"fmt"
	"regexp"
	"strings"
)

type negativeControl struct {
	label    string
	expected string
	mutate   func(ci, release map[string]interface{}, packText string) string
}

func nativeSteps(ci map[string]interface{}) []interface{} {
	return asList(asMap(jobsOf(ci)["native"])["steps"])
}

func releaseDesktopSteps(release map[string]interface{}) []interface{} {
	return asList(asMap(jobsOf(release)["desktop"])["steps"])
}

func popJobIf(release map[string]interface{}, jobKeys ...string) {
	jobs := jobsOf(release)
	for _, jobKey := range jobKeys {
		delete(asMap(jobs[jobKey]), "if")
	}
}

func insertStepBefore(job map[string]interface{}, namePrefix string, step map[string]interface{}) {
	steps := asList(job["steps"])
	for index, rawStep := range steps {
		if strings.HasPrefix(stepName(asMap(rawStep)), namePrefix) {
			steps = append(steps, nil)
			copy(steps[index+1:], steps[index:])
			steps[index] = step
			job["steps"] = steps
			return
		}
	}
	panic(fmt.Sprintf("step not found before %s", namePrefix))
}

var skipFrontendRestore = regexp.MustCompile(`&& '--consume-dist=[^']*'`)

var negativeControls = []negativeControl{
	{"round-1 upload name order regression is caught",
		"resolver required artifacts are all produced by CI",
		func(ci, release map[string]interface{}, packText string) string {
			for _, rawStep := range nativeSteps(ci) {
				step := asMap(rawStep)
				if strings.HasPrefix(str(step["uses"]), "actions/upload-artifact") {
					asMap(step["with"])["name"] = "native-${{ matrix.kind }}-${{ matrix.os }}-${{ matrix.arch }}"
				}
			}
			return packText
		}},
	{"round-1 native download path regression is caught",
		"native download stages outside target/ (desktop)",
		func(ci, release map[string]interface{}, packText string) string {
			for _, jobKey := range []string{"desktop", "server"} {
				step := findNativeDownloadStep(release, jobKey)
				asMap(step["with"])["path"] = "target/go-build"
			}
			return packText
		}},
	{"ci-source validation removal is caught",
		"ci-source validates the release tag",
		func(ci, release map[string]interface{}, packText string) string {
			steps := asList(asMap(jobsOf(release)["ci-source"])["steps"])
			for _, rawStep := range steps {
				step := asMap(rawStep)
				if strings.Contains(stepName(step), "The tag drives the release version") {
					step["run"] = "true"
				}
			}
			return packText
		}},
	{"lazycat placeholder validation removal is caught",
		"ci-source validates the release tag and the 0.0.0 version placeholders",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(release)["ci-source"]), "The tag drives the release version")
			step["run"] = strings.ReplaceAll(stepRun(step), "lazycat/package.yml", "lazycat/package.json")
			return packText
		}},
	{"packaging gate if removal is caught",
		"job graph: reuse: successful ci-source",
		func(ci, release map[string]interface{}, packText string) string {
			popJobIf(release, "desktop", "server", "agent", "publish")
			return packText
		}},
	{"publish gate if removal is caught",
		"job graph: reuse: successful ci-source",
		func(ci, release map[string]interface{}, packText string) string {
			popJobIf(release, "publish")
			return packText
		}},
	{"windows shell removal is caught",
		"release.yml windows bash-syntax steps declare shell: bash",
		func(ci, release map[string]interface{}, packText string) string {
			for _, rawStep := range releaseDesktopSteps(release) {
				step := asMap(rawStep)
				if stepName(step) == "Install the pinned Wails CLI" {
					delete(step, "shell")
				}
			}
			return packText
		}},
	{"duplicate isolated Go group definitions are caught",
		"quality runs three ordered isolated Go groups",
		func(ci, release map[string]interface{}, packText string) string {
			qualityJob := asMap(jobsOf(ci)["quality"])
			goTest := findStep(qualityJob, "Go tests")
			insertStepBefore(qualityJob, "Wails bindings drift",
				map[string]interface{}{"name": "Duplicate Go tests", "run": stepRun(goTest)})
			return packText
		}},
	{"frontend dist download removal is caught",
		"native job downloads the verified frontend dist for every desktop build",
		func(ci, release map[string]interface{}, packText string) string {
			steps := nativeSteps(ci)
			for i, rawStep := range steps {
				step := asMap(rawStep)
				with := asMap(step["with"])
				if with != nil &&
					strings.HasPrefix(str(step["uses"]), "actions/download-artifact") &&
					str(with["name"]) == "frontend-dist" {
					nativeJob := asMap(jobsOf(ci)["native"])
					nativeJob["steps"] = append(steps[:i], steps[i+1:]...)
					return packText
				}
			}
			return packText
		}},
	{"desktop skip-frontend restore is caught",
		"desktop production build consumes the downloaded verified dist",
		func(ci, release map[string]interface{}, packText string) string {
			for _, rawStep := range nativeSteps(ci) {
				step := asMap(rawStep)
				if strings.Contains(stepName(step), "Build the repro-checked production binary") {
					step["run"] = skipFrontendRestore.ReplaceAllString(stepRun(step), "&& '--skip-frontend'")
				}
			}
			return packText
		}},
	{"minimumReleaseAge env bypass re-addition is caught",
		"ci.yml carries no pnpm minimumReleaseAge bypass",
		func(ci, release map[string]interface{}, packText string) string {
			env := asMap(ci["env"])
			if env == nil {
				env = map[string]interface{}{}
				ci["env"] = env
			}
			env["pnpm_config_minimum_release_age"] = 0
			return packText
		}},
	{"minimumReleaseAge install flag re-addition is caught",
		"ci.yml carries no pnpm minimumReleaseAge bypass",
		func(ci, release map[string]interface{}, packText string) string {
			for _, rawStep := range asList(asMap(jobsOf(ci)["frontend-quality"])["steps"]) {
				step := asMap(rawStep)
				if strings.Contains(stepRun(step), "pnpm install") {
					step["run"] = stepRun(step) + " --config.minimumReleaseAge=0"
					return packText
				}
			}
			return packText
		}},
	{"e2e staging reuse-only condition is caught",
		"server stages and verifies the CI e2e report on every release path",
		func(ci, release map[string]interface{}, packText string) string {
			findStep(asMap(jobsOf(release)["server"]), "Stage and verify the reused CI e2e report")["if"] = "needs.ci-source.outputs.reused == 'true'"
			return packText
		}},
	{"xargs upload restore is caught",
		"release publish uploads through release-upload.mjs",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(release)["publish"]), "Create or update the draft release")
			step["run"] = "printf '%s\\0' candidate/* | xargs -0 -P 8 -n 1 gh release upload \"$GITHUB_REF_NAME\" --clobber"
			return packText
		}},
	{"setup-go cache-hit skip re-addition is caught",
		"desktop setup-go is never skipped by the wails cache hit",
		func(ci, release map[string]interface{}, packText string) string {
			for _, rawStep := range releaseDesktopSteps(release) {
				step := asMap(rawStep)
				if strings.HasPrefix(str(step["uses"]), "actions/setup-go") {
					step["if"] = "matrix.os != 'windows' || steps.wails-cache.outputs.cache-hit != 'true'"
				}
			}
			return packText
		}},
	{"quality-source expected ref drift is caught",
		"quality-source locks reusable quality evidence to master",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(ci)["quality-source"]), "Resolve reusable quality evidence")
			asMap(step["env"])["CI_REUSE_EXPECTED_REF"] = "main"
			return packText
		}},
	{"quality-source tag guard inversion is caught",
		"quality-source calls the resolver only for tags",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(ci)["quality-source"]), "Resolve reusable quality evidence")
			step["run"] = strings.Replace(stepRun(step), `if [ "$GITHUB_REF_TYPE" != "tag" ]; then`, `if [ "$GITHUB_REF_TYPE" = "tag" ]; then`, 1)
			return packText
		}},
	{"quality-source non-tag fallback removal is caught",
		"quality-source calls the resolver only for tags",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(ci)["quality-source"]), "Resolve reusable quality evidence")
			step["run"] = strings.Replace(stepRun(step), `echo "reused=false" >> "$GITHUB_OUTPUT"`, `echo "reused=true" >> "$GITHUB_OUTPUT"`, 1)
			return packText
		}},
	{"unguarded quality-source resolver call is caught",
		"quality-source calls the resolver only for tags",
		func(ci, release map[string]interface{}, packText string) string {
			insertStepBefore(asMap(jobsOf(ci)["quality-source"]), "Resolve reusable quality evidence",
				map[string]interface{}{"name": "Unguarded resolver", "run": "bash .github/scripts/resolve-ci-reuse.sh"})
			return packText
		}},
	{"quality reused-evidence condition removal is caught",
		"quality runs only when reusable master quality evidence is absent",
		func(ci, release map[string]interface{}, packText string) string {
			delete(asMap(jobsOf(ci)["quality"]), "if")
			return packText
		}},
	{"postgres reused-evidence condition removal is caught",
		"postgres runs only when reusable master quality evidence is absent",
		func(ci, release map[string]interface{}, packText string) string {
			delete(asMap(jobsOf(ci)["postgres"]), "if")
			return packText
		}},
	{"native frontend dependency removal is caught",
		"native depends only on frontend and version",
		func(ci, release map[string]interface{}, packText string) string {
			asMap(jobsOf(ci)["native"])["needs"] = "version"
			return packText
		}},
	{"mobile frontend dependency removal is caught",
		"mobile depends only on frontend and version",
		func(ci, release map[string]interface{}, packText string) string {
			asMap(jobsOf(ci)["mobile"])["needs"] = "version"
			return packText
		}},
	{"ios-dist success-only gate is caught",
		"ios-dist continues past skipped quality",
		func(ci, release map[string]interface{}, packText string) string {
			asMap(jobsOf(ci)["ios-dist"])["if"] = "${{ success() && github.event_name == 'push' && vars.NEXTERM_IOS_SIGNING == 'true' }}"
			return packText
		}},
	{"ios-dist failure guard removal is caught",
		"ios-dist continues past skipped quality",
		func(ci, release map[string]interface{}, packText string) string {
			asMap(jobsOf(ci)["ios-dist"])["if"] = "${{ !cancelled() && github.event_name == 'push' && vars.NEXTERM_IOS_SIGNING == 'true' }}"
			return packText
		}},
	{"frontend dist manifest upload removal is caught",
		"frontend job is the only frontend-dist uploader",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(ci)["frontend"]), "Upload the verified frontend dist")
			asMap(step["with"])["path"] = "dist/"
			return packText
		}},
	{"frontend-quality lint removal is caught",
		"frontend-quality keeps the full pnpm lint suite",
		func(ci, release map[string]interface{}, packText string) string {
			findStep(asMap(jobsOf(ci)["frontend-quality"]), "Frontend lint")["run"] = "true"
			return packText
		}},
	{"frontend-quality test removal is caught",
		"frontend-quality keeps the full pnpm test suite",
		func(ci, release map[string]interface{}, packText string) string {
			findStep(asMap(jobsOf(ci)["frontend-quality"]), "Frontend tests")["run"] = "true"
			return packText
		}},
	{"frontend-quality reused-evidence condition removal is caught",
		"frontend-quality runs only when reusable master quality evidence is absent",
		func(ci, release map[string]interface{}, packText string) string {
			delete(asMap(jobsOf(ci)["frontend-quality"]), "if")
			return packText
		}},
	{"ios-dist frontend-quality dependency drift is caught",
		"ios-dist keeps the reused-quality dependency graph",
		func(ci, release map[string]interface{}, packText string) string {
			asMap(jobsOf(ci)["ios-dist"])["needs"] = []interface{}{"quality-source", "quality", "frontend", "version"}
			return packText
		}},
	{"release download outside resolver required artifacts is caught",
		"release downloads are a subset of resolver required artifacts",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(release)["mobile"]), "Download the verified mobile package")
			asMap(step["with"])["name"] = "mobile-ios-dist"
			return packText
		}},
	{"mobile packaging gate if removal is caught",
		"job graph: reuse: successful ci-source",
		func(ci, release map[string]interface{}, packText string) string {
			popJobIf(release, "mobile")
			return packText
		}},
	{"publish mobile dependency removal is caught",
		"job graph: reuse: mobile packaging failure",
		func(ci, release map[string]interface{}, packText string) string {
			asMap(jobsOf(release)["publish"])["needs"] = []interface{}{"desktop", "server", "agent"}
			return packText
		}},
	{"native warm-cache master condition removal is caught",
		"native warm caches and tool setup pin master, Windows and desktop",
		func(ci, release map[string]interface{}, packText string) string {
			findStepByID(asMap(jobsOf(ci)["native"]), "wails-cache")["if"] = "matrix.os == 'windows' && matrix.kind == 'desktop'"
			return packText
		}},
	{"native NSIS cache key drift is caught",
		"native warm caches use exact Wails and NSIS keys",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStepByID(asMap(jobsOf(ci)["native"]), "nsis-cache")
			asMap(step["with"])["key"] = "nsis-latest"
			return packText
		}},
	{"native NSIS cache-miss guard inversion is caught",
		"native warm caches install only on misses",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(ci)["native"]), "Install and verify the Windows release tools")
			step["run"] = strings.Replace(stepRun(step),
				`if [ "${{ steps.nsis-cache.outputs.cache-hit }}" != "true" ]; then`,
				`if [ "${{ steps.nsis-cache.outputs.cache-hit }}" = "true" ]; then`, 1)
			return packText
		}},
	{"native Wails version verification removal is caught",
		"native warm caches install only on misses",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(ci)["native"]), "Install and verify the Windows release tools")
			step["run"] = strings.Replace(stepRun(step), "wails3 version", "", 1)
			return packText
		}},
	{"release NSIS cache key drift is caught",
		"release desktop uses exact Windows Wails and NSIS cache entries",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStepByID(asMap(jobsOf(release)["desktop"]), "nsis-cache")
			asMap(step["with"])["key"] = "nsis-latest"
			return packText
		}},
	{"release NSIS cache-miss guard inversion is caught",
		"release desktop installs Wails and NSIS only on cache misses",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(release)["desktop"]), "Install NSIS")
			step["run"] = strings.Replace(stepRun(step),
				`if [ "${{ steps.nsis-cache.outputs.cache-hit }}" != "true" ]; then`,
				`if [ "${{ steps.nsis-cache.outputs.cache-hit }}" = "true" ]; then`, 1)
			return packText
		}},
	{"release NSIS version verification removal is caught",
		"release desktop installs Wails and NSIS only on cache misses",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(release)["desktop"]), "Install NSIS")
			step["run"] = strings.Replace(stepRun(step), `"/c/Program Files (x86)/NSIS/makensis.exe" /VERSION`, "", 1)
			return packText
		}},
	{"PR Windows job condition removal is caught",
		"PR Windows smoke is pull-request only",
		func(ci, release map[string]interface{}, packText string) string {
			delete(asMap(jobsOf(ci)["pr-windows-smoke"]), "if")
			return packText
		}},
}

type resolverNegativeControl struct {
	label    string
	expected string
	mutate   func(resolverText string) string
}

var resolverNegativeControls = []resolverNegativeControl{
	{"agent required artifact deletion is caught by expanded release downloads",
		"release downloads are a subset of resolver required artifacts",
		func(resolverText string) string {
			return strings.Replace(resolverText, " native-linux-amd64-agent", "", 1)
		}},
}

type windowsNegativeControl struct {
	label    string
	expected string
	mutate   func(windowsRealText string) string
}

var windowsNegativeControls = []windowsNegativeControl{
	{"Windows targeted-test SKIP guard removal is caught",
		"real Windows targeted tests reject SKIP",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, "if ($skips.Count -ne 0)", "if ($false)", 1)
		}},
	{"Windows local second-volume fixture removal is caught",
		"real Windows fixtures cover local, WinRM and OpenSSH second-volume paths",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, "Invoke-TargetedGoTest -Package './internal/fs/local' -Test 'TestRealWindowsSecondVolume'", "", 1)
		}},
	{"WinRM cwd and path subtest filter removal is caught",
		"real WinRM loopback runs the complete cwd and quoted-path subtest set",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, " -RunExpression '^TestRealWindowsServer$/^(cwd_persistence|filesystem|relative_read_write_and_backup_after_cwd_change)$'", "", 1)
		}},
	{"WinRM temporary listener selector removal is caught",
		"real WinRM listeners are created with selector sets",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, "New-WSManInstance -ResourceURI winrm/config/listener -SelectorSet", "New-WSManInstance -ResourceURI winrm/config/listener -ValueSet", 1)
		}},
	{"WinRM restoration listener selector removal is caught",
		"real WinRM listeners are created with selector sets",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, "New-WSManInstance -ResourceURI winrm/config/listener -SelectorSet $selectorSet -ValueSet $valueSet", "New-WSManInstance -ResourceURI winrm/config/listener -ValueSet $valueSet", 1)
		}},
	{"OpenSSH config array parenthesis removal is caught",
		"real OpenSSH fixture writes multiline configuration",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, "('Port ' + $sshPort),", "'Port ' + $sshPort,", 1)
		}},
	{"OpenSSH host key inheritance protection removal is caught",
		"real OpenSSH fixture restricts the host key ACL",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, " /inheritance:r", "", 1)
		}},
	{"OpenSSH host key creator ACE removal is caught",
		"real OpenSSH fixture restricts the host key ACL",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, " /remove:g ('*' + $currentUserSid)", "", 1)
		}},
	{"OpenSSH service startup removal is caught",
		"real OpenSSH fixture runs under a LocalSystem service",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, "$null = New-Service -Name $serviceName -BinaryPathName $binaryPath -StartupType Manual", "", 1)
		}},
	{"Windows Git Bash and System32 PATH order reversal is caught",
		"real Windows Go tests select Git Bash and System32 whoami",
		func(windowsRealText string) string {
			return strings.Replace(windowsRealText, "$env:PATH = $gitBin + ';' + $system32 + ';' + $env:PATH", "$env:PATH = $system32 + ';' + $gitBin + ';' + $env:PATH", 1)
		}},
}

func runNegativeControls(ciRaw, releaseRaw []byte, in inputs) []string {
	var missed []string
	report := func(label, expected string, results []checkResult) {
		caught := false
		for _, result := range results {
			if strings.Contains(result.name, expected) && !result.ok {
				caught = true
				break
			}
		}
		if caught {
			fmt.Printf("ok - negative control: %s\n", label)
		} else {
			fmt.Printf("FAIL - negative control: %s\n", label)
		}
		if !caught {
			missed = append(missed, fmt.Sprintf("negative control: %s: mutation not detected by %q", label, expected))
		}
	}
	for _, control := range negativeControls {
		ci := parseYAML(ciRaw)
		release := parseYAML(releaseRaw)
		mutatedPack := control.mutate(ci, release, in.packText)
		report(control.label, control.expected, runWiringChecks(inputs{
			ci:              ci,
			release:         release,
			resolverText:    in.resolverText,
			packText:        mutatedPack,
			uploadText:      in.uploadText,
			evidenceText:    in.evidenceText,
			windowsRealText: in.windowsRealText,
			root:            in.root,
		}))
	}
	for _, control := range resolverNegativeControls {
		mutated := in
		mutated.resolverText = control.mutate(in.resolverText)
		report(control.label, control.expected, runWiringChecks(mutated))
	}
	for _, control := range windowsNegativeControls {
		mutated := in
		mutated.windowsRealText = control.mutate(in.windowsRealText)
		report(control.label, control.expected, runWiringChecks(mutated))
	}
	return missed
}
