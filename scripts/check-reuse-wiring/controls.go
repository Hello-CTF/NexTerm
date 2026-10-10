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

func qualitySteps(ci map[string]interface{}) []interface{} {
	return asList(asMap(jobsOf(ci)["quality"])["steps"])
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

func popStepIf(release map[string]interface{}, jobKey, stepNamePart string) {
	delete(findStep(asMap(jobsOf(release)[jobKey]), stepNamePart), "if")
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
			popJobIf(release, "desktop", "server", "publish")
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
	{"standalone pnpm typecheck re-addition is caught",
		"quality has no standalone pnpm typecheck step",
		func(ci, release map[string]interface{}, packText string) string {
			insertStepBefore(asMap(jobsOf(ci)["quality"]), "Frontend lint and tests",
				map[string]interface{}{"name": "Frontend typecheck", "run": "pnpm typecheck"})
			return packText
		}},
	{"plain go test re-addition is caught",
		"quality runs the Go suite once without -race",
		func(ci, release map[string]interface{}, packText string) string {
			insertStepBefore(asMap(jobsOf(ci)["quality"]), "Go tests",
				map[string]interface{}{"name": "Go tests", "run": "go test -mod=readonly ./..."})
			return packText
		}},
	{"race re-instrumentation of the quality suite is caught",
		"quality runs the Go suite once without -race",
		func(ci, release map[string]interface{}, packText string) string {
			for _, rawStep := range qualitySteps(ci) {
				step := asMap(rawStep)
				if strings.HasPrefix(stepName(step), "Go tests") {
					step["run"] = strings.Replace(stepRun(step), "go test -mod=readonly", "go test -mod=readonly -race", 1)
					return packText
				}
			}
			return packText
		}},
	{"smoke desktop step re-addition is caught",
		"native job builds and runs no smoke desktop binary",
		func(ci, release map[string]interface{}, packText string) string {
			steps := nativeSteps(ci)
			nativeJob := asMap(jobsOf(ci)["native"])
			nativeJob["steps"] = append([]interface{}{map[string]interface{}{
				"name": "Build the smoke desktop binary and run the native smoke",
				"if":   "matrix.kind == 'desktop'",
				"run":  "node scripts/build.mjs desktop --release --smoke --os=${{ matrix.os }} --arch=${{ matrix.arch }}",
			}}, steps...)
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
	{"xvfb reintroduction is caught",
		"native job installs no virtual display for removed smoke runs",
		func(ci, release map[string]interface{}, packText string) string {
			for _, rawStep := range nativeSteps(ci) {
				step := asMap(rawStep)
				if strings.Contains(stepRun(step), "apt-get install") {
					step["run"] = strings.Replace(stepRun(step), "libwebkitgtk-6.0-dev", "libwebkitgtk-6.0-dev xvfb", 1)
				}
			}
			return packText
		}},
	{"production-tagged Go suite re-addition is caught",
		"quality runs no production-tagged Go suite",
		func(ci, release map[string]interface{}, packText string) string {
			insertStepBefore(asMap(jobsOf(ci)["quality"]), "LazyCat manifest injects",
				map[string]interface{}{"name": "Go production tests with embedded desktop assets",
					"run": "go test -mod=readonly -tags production ./..."})
			return packText
		}},
	{"build harness unit test re-addition is caught",
		"quality runs no build harness unit tests",
		func(ci, release map[string]interface{}, packText string) string {
			insertStepBefore(asMap(jobsOf(ci)["quality"]), "Wails bindings drift",
				map[string]interface{}{"name": "Build harness unit tests", "run": "node --test scripts/lib/*.test.mjs"})
			return packText
		}},
	{"standalone windows job re-addition is caught",
		"ci.yml drops the browser acceptance and standalone windows native jobs",
		func(ci, release map[string]interface{}, packText string) string {
			jobsOf(ci)["ssh-windows"] = map[string]interface{}{
				"name": "Windows SSH native tests", "runs-on": "windows-latest",
				"steps": []interface{}{map[string]interface{}{"uses": "actions/checkout@v7"}},
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
			for _, rawStep := range qualitySteps(ci) {
				step := asMap(rawStep)
				if strings.Contains(stepRun(step), "pnpm install") {
					step["run"] = stepRun(step) + " --config.minimumReleaseAge=0"
					return packText
				}
			}
			return packText
		}},
	{"duplicate pack evidence report is caught",
		"pack generates the server archive evidence report exactly once",
		func(ci, release map[string]interface{}, packText string) string {
			return strings.ReplaceAll(packText,
				`(cd "$ROOT" && node scripts/build.mjs report "${report_args[@]}")`,
				`(cd "$ROOT" && node scripts/build.mjs report "${report_args[@]}")`+"\n"+
					`(cd "$ROOT" && node scripts/build.mjs report --kind=server-archive --flavor=full --os=linux "--arch=$ARCH" "--file=$ARTIFACT")`)
		}},
	{"e2e staging if removal is caught",
		"server stages the reused CI e2e report only on reuse-hit",
		func(ci, release map[string]interface{}, packText string) string {
			popStepIf(release, "server", "Stage and verify the reused CI e2e report")
			return packText
		}},
	{"e2e exercise if removal is caught",
		"server reruns e2e only without reuse",
		func(ci, release map[string]interface{}, packText string) string {
			popStepIf(release, "server", "Exercise the packaged server before archiving")
			return packText
		}},
	{"xargs upload restore is caught",
		"release publish uploads through release-upload.mjs",
		func(ci, release map[string]interface{}, packText string) string {
			step := findStep(asMap(jobsOf(release)["publish"]), "Create or update the draft release")
			step["run"] = "printf '%s\\0' candidate/* | xargs -0 -P 8 -n 1 gh release upload \"$GITHUB_REF_NAME\" --clobber"
			return packText
		}},
	{"wails cache order regression is caught",
		"windows wails cache restores before setup-go",
		func(ci, release map[string]interface{}, packText string) string {
			steps := releaseDesktopSteps(release)
			index := -1
			for i, rawStep := range steps {
				if asMap(rawStep)["id"] == "wails-cache" {
					index = i
					break
				}
			}
			step := steps[index]
			rest := append(steps[:index], steps[index+1:]...)
			setupGoIndex := -1
			for i, rawStep := range rest {
				if strings.HasPrefix(str(asMap(rawStep)["uses"]), "actions/setup-go") {
					setupGoIndex = i
					break
				}
			}
			reordered := append(rest[:setupGoIndex+1], step)
			reordered = append(reordered, rest[setupGoIndex+1:]...)
			jobsOf(release)["desktop"].(map[string]interface{})["steps"] = reordered
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
}

func runNegativeControls(ciRaw, releaseRaw []byte, in inputs) []string {
	var missed []string
	for _, control := range negativeControls {
		ci := parseYAML(ciRaw)
		release := parseYAML(releaseRaw)
		mutatedPack := control.mutate(ci, release, in.packText)
		results := runWiringChecks(inputs{
			ci:           ci,
			release:      release,
			resolverText: in.resolverText,
			packText:     mutatedPack,
			uploadText:   in.uploadText,
			evidenceText: in.evidenceText,
			root:         in.root,
		})
		caught := false
		for _, result := range results {
			if strings.Contains(result.name, control.expected) && !result.ok {
				caught = true
				break
			}
		}
		if caught {
			fmt.Printf("ok - negative control: %s\n", control.label)
		} else {
			fmt.Printf("FAIL - negative control: %s\n", control.label)
		}
		if !caught {
			missed = append(missed, fmt.Sprintf("negative control: %s: mutation not detected by %q", control.label, control.expected))
		}
	}
	return missed
}
