package ciworkflow

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowFile struct {
	On   workflowTriggers       `yaml:"on"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowTriggers struct {
	Push         workflowTrigger  `yaml:"push"`
	PullRequest  *workflowTrigger `yaml:"pull_request"`
	WorkflowCall struct {
		Inputs  map[string]workflowInput  `yaml:"inputs"`
		Secrets map[string]workflowSecret `yaml:"secrets"`
	} `yaml:"workflow_call"`
}

type workflowTrigger struct {
	Branches []string `yaml:"branches"`
	Paths    []string `yaml:"paths"`
	Types    []string `yaml:"types"`
}

type workflowInput struct {
	Required bool   `yaml:"required"`
	Type     string `yaml:"type"`
}

type workflowSecret struct {
	Required bool `yaml:"required"`
}

type workflowJob struct {
	Name           string            `yaml:"name"`
	If             string            `yaml:"if"`
	Needs          stringList        `yaml:"needs"`
	Uses           string            `yaml:"uses"`
	With           map[string]any    `yaml:"with"`
	RunsOn         string            `yaml:"runs-on"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Permissions    map[string]string `yaml:"permissions"`
	Env            map[string]string `yaml:"env"`
	Strategy       struct {
		Matrix workflowMatrix `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []workflowStep `yaml:"steps"`
}

type workflowMatrix struct {
	Expression string
	Include    []map[string]any
}

func (matrix *workflowMatrix) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		matrix.Expression = node.Value
		return nil
	}
	var decoded struct {
		Include []map[string]any `yaml:"include"`
	}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	matrix.Include = decoded.Include
	return nil
}

type workflowStep struct {
	ID   string            `yaml:"id"`
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]any    `yaml:"with"`
}

type stringList []string

func (values *stringList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*values = stringList{node.Value}
		return nil
	}
	var decoded []string
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*values = decoded
	return nil
}

type taskFile struct {
	Tasks map[string]taskDefinition `yaml:"tasks"`
}

type taskDefinition struct {
	Deps     []taskCommand     `yaml:"deps"`
	Commands []taskCommand     `yaml:"cmds"`
	Env      map[string]string `yaml:"env"`
}

type taskCommand struct {
	Run  string
	Task string
	Vars map[string]string
}

func (command *taskCommand) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		command.Run = node.Value
		return nil
	}
	var decoded struct {
		Task string            `yaml:"task"`
		Vars map[string]string `yaml:"vars"`
	}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	command.Task = decoded.Task
	command.Vars = decoded.Vars
	return nil
}

func TestQualityStaysStaticAndRequiredUnitOwnsGoTests(t *testing.T) {
	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	quality := requireJob(t, ci, "quality")
	pnpm := requireUse(t, quality, "pnpm/action-setup@")
	install := requireRun(t, quality, "pnpm install --frozen-lockfile")
	qualityGate := requireRun(t, quality, "wails3 task check:ci-quality")
	requireBefore(t, quality, pnpm, install, "pnpm must be set up before installing quality dependencies")
	requireBefore(t, quality, install, qualityGate, "frontend dependencies must be installed before the quality gate")
	windowsVet := requireRun(t, quality, "go vet ./...")
	if got := windowsVet.Env["GOOS"]; got != "windows" {
		t.Fatalf("Windows vet GOOS = %q, want windows", got)
	}
	for _, step := range quality.Steps {
		if run := normalize(step.Run); strings.Contains(run, "go test") {
			t.Errorf("quality job duplicates Go tests owned by the required unit job: %s", run)
		}
	}
	unit := requireJob(t, ci, "unit")
	if got := expression(unit.If); got != "fromJSON(needs.version.outputs.scope).unit" {
		t.Fatalf("unit job must run on PRs rather than repeat for release tags, got condition %q", got)
	}
	requireRun(t, unit, "go test -race -count=1 -p=${RACE_PACKAGE_PARALLELISM} ./...")

	tasks := loadTaskfile(t)
	commands := make(map[string]int)
	for _, command := range tasks.Tasks["check:ci-quality"].Commands {
		if command.Task != "" {
			commands["task:"+command.Task]++
		} else {
			commands["run:"+normalize(command.Run)]++
		}
		if strings.Contains(command.Run, "go test") {
			t.Errorf("check:ci-quality must remain a static gate rather than a Go unit-test entry: %s", command.Run)
		}
	}
	for _, required := range []string{
		"task:fmt:check",
		"run:go mod verify",
		"run:go run ./scripts/verify-manifest-injects",
		"task:ipc:contract:check",
		"task:gorm:check",
		"task:check:scripts",
		"task:vet",
		"task:bindings:check",
	} {
		if commands[required] != 1 {
			t.Errorf("check:ci-quality command %s count = %d, want 1", required, commands[required])
		}
	}

	scriptCommands := make(map[string]int)
	for _, command := range tasks.Tasks["check:scripts"].Commands {
		scriptCommands[normalize(command.Run)]++
		if strings.Contains(command.Run, "go test") {
			t.Errorf("check:scripts duplicates Go tests owned by task test: %s", command.Run)
		}
	}
	for _, required := range []string{
		"go run ./scripts/test-database --check",
		"node scripts/check-placeholders.mjs",
		"node --test scripts/*.test.mjs",
		"node --test .github/scripts/*.test.mjs",
		"python3 scripts/test-systemd-install.py",
		"go run ./scripts/check-release-evidence",
	} {
		if scriptCommands[required] != 1 {
			t.Errorf("check:scripts command %s count = %d, want 1", required, scriptCommands[required])
		}
	}

	checkCommands := make(map[string]int)
	for _, command := range tasks.Tasks["check"].Commands {
		checkCommands[command.Task]++
	}
	if checkCommands["test"] != 1 {
		t.Errorf("task check must run the independent full Go test task exactly once, got %d", checkCommands["test"])
	}
	testCommands := tasks.Tasks["test"].Commands
	if len(testCommands) != 1 || normalize(testCommands[0].Run) != "go test ./..." {
		t.Fatalf("task test no longer owns the full Go unit run: %#v", testCommands)
	}
}

func TestRaceDefaultsToFullUncachedRunAndVerifyDoesNotNarrowIt(t *testing.T) {
	tasks := loadTaskfile(t)
	race := tasks.Tasks["test:race"]
	const selectedRace = `go test -race -count=1 -p={{.RACE_PACKAGE_PARALLELISM | default "4"}} {{.RACE_PACKAGES | default "./..."}}`
	if len(race.Commands) != 1 || normalize(race.Commands[0].Run) != selectedRace {
		t.Fatalf("test:race must select packages and bounded parallelism for an uncached full run: %#v", race.Commands)
	}

	raceCalls := 0
	for _, command := range tasks.Tasks["verify"].Commands {
		if command.Task != "test:race" {
			continue
		}
		raceCalls++
		if len(command.Vars) != 0 {
			t.Errorf("verify must not narrow its final full race run: %v", command.Vars)
		}
	}
	if raceCalls != 1 {
		t.Errorf("verify test:race call count = %d, want 1", raceCalls)
	}

	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	if len(ci.On.Push.Branches) != 0 {
		t.Errorf("CI must not run on every push: %v", ci.On.Push.Branches)
	}
	if ci.On.PullRequest == nil {
		t.Fatal("CI must run on pull requests")
	}
	ciRace := requireJob(t, ci, "unit")
	if got := ciRace.If; got != "fromJSON(needs.version.outputs.scope).unit" {
		t.Fatalf("race must run on PRs without repeating for release tags, got %q", got)
	}
	if got := ciRace.Env["RACE_PACKAGE_PARALLELISM"]; got != "4" {
		t.Fatalf("CI race package parallelism = %q, want the evidenced default 4", got)
	}
	requireRun(t, ciRace, "go test -race -count=1 -p=${RACE_PACKAGE_PARALLELISM} ./...")
}

func TestLocalFrontendAndProductionReuseVerifiedResults(t *testing.T) {
	tasks := loadTaskfile(t)
	frontend := tasks.Tasks["check:frontend"]
	commands := make(map[string]int)
	for _, command := range frontend.Commands {
		if command.Task != "" {
			commands["task:"+command.Task]++
		} else {
			commands["run:"+normalize(command.Run)]++
		}
	}
	if commands["run:pnpm test"] != 0 {
		t.Error("check:frontend still duplicates the Vitest suite without coverage")
	}
	for _, required := range []string{"run:pnpm lint", "run:pnpm test:coverage", "task:frontend"} {
		if commands[required] != 1 {
			t.Errorf("check:frontend command %s count = %d, want 1", required, commands[required])
		}
	}
	var packageJSON struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(readRepositoryFile(t, "package.json")), &packageJSON); err != nil {
		t.Fatal(err)
	}
	if packageJSON.Scripts["test"] != "vitest run" || packageJSON.Scripts["test:coverage"] != "vitest run --coverage" {
		t.Fatalf("frontend package scripts changed: %v", packageJSON.Scripts)
	}

	production := tasks.Tasks["test:production"]
	if production.Env["DIST_SOURCE"] != "{{.DIST_SOURCE}}" || production.Env["DIST_MANIFEST"] != "{{.DIST_MANIFEST}}" {
		t.Fatalf("test:production does not forward dist consumption settings: %v", production.Env)
	}
	verify := tasks.Tasks["verify"]
	checkSeen := false
	productionSeen := false
	for _, command := range verify.Commands {
		if command.Task == "check" {
			checkSeen = true
		}
		if command.Task != "test:production" {
			continue
		}
		if productionSeen {
			t.Fatal("verify runs test:production more than once")
		}
		productionSeen = true
		if !checkSeen {
			t.Error("verify runs test:production before check produces the verified dist")
		}
		if command.Vars["DIST_SOURCE"] != "dist" || command.Vars["DIST_MANIFEST"] != "target/dist-manifest.json" {
			t.Errorf("verify test:production vars = %v, want checked dist and manifest", command.Vars)
		}
	}
	if !checkSeen || !productionSeen {
		t.Fatalf("verify check/production contract missing: check=%t production=%t", checkSeen, productionSeen)
	}

	desktopSeen := false
	helperMatrixSeen := 0
	for _, command := range verify.Commands {
		if command.Task == "helpers:matrix" {
			helperMatrixSeen++
		}
		if normalize(command.Run) == "node scripts/build.mjs desktop --release --repro-check --consume-dist=dist --dist-manifest=target/dist-manifest.json" {
			desktopSeen = true
		}
	}
	if helperMatrixSeen != 1 {
		t.Errorf("verify helper matrix count = %d, want 1", helperMatrixSeen)
	}
	helperMatrix := tasks.Tasks["helpers:matrix"].Commands
	if len(helperMatrix) != 1 || normalize(helperMatrix[0].Run) != "node scripts/build.mjs helpers --release" {
		t.Fatalf("helpers:matrix must build the explicit nine-target matrix: %#v", helperMatrix)
	}
	if !desktopSeen {
		t.Error("verify no longer retains the final manifest-checked desktop build")
	}
}

func TestNativeJobsOwnPlatformCompilationAndProductionTests(t *testing.T) {
	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	native := requireJob(t, ci, "native")
	production := requireRun(t, native, "node scripts/test-production.mjs")
	if production.Env["DIST_SOURCE"] != "target/ci-work/verified-dist/dist" || production.Env["DIST_MANIFEST"] != "target/ci-work/verified-dist/target/dist-manifest.json" {
		t.Fatalf("production assets are not manifest-verified: %v", production.Env)
	}
	compile := requireRun(t, native, "go run -mod=readonly ./scripts/compile-only ./...")
	if !strings.Contains(compile.If, "github.event_name == 'pull_request'") {
		t.Fatalf("test compilation repeats during release: %q", compile.If)
	}
	for _, job := range ci.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(normalize(step.Run), "go test -run '^$'") {
				t.Error("compile-only must not execute TestMain")
			}
		}
	}
}

func TestCIOKAggregatesAllJobsAndRejectsUnsuccessfulResults(t *testing.T) {
	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	aggregator := requireJob(t, ci, "ci-ok")
	if expression(aggregator.If) != "always()" {
		t.Fatalf("ci-ok condition = %q", aggregator.If)
	}
	actualNeeds := make(map[string]bool)
	for _, need := range aggregator.Needs {
		actualNeeds[need] = true
	}
	for name := range ci.Jobs {
		if name != "ci-ok" && !actualNeeds[name] {
			t.Errorf("ci-ok does not need %s", name)
		}
		delete(actualNeeds, name)
	}
	for extra := range actualNeeds {
		t.Errorf("ci-ok needs unknown job %s", extra)
	}

	var aggregateStep *workflowStep
	for index := range aggregator.Steps {
		if aggregator.Steps[index].Env["NEEDS"] == "${{ toJSON(needs) }}" {
			aggregateStep = &aggregator.Steps[index]
			break
		}
	}
	if aggregateStep == nil {
		t.Fatal("ci-ok aggregation step not found")
	}
	script := extractNodeScript(t, aggregateStep.Run)
	scenarios := []struct {
		name           string
		event          string
		iosSigning     string
		releaseVersion string
		scope          string
		results        map[string]string
		wantSuccess    bool
		wantOutput     string
	}{
		{
			name:  "documentation PR skips only the planned application jobs",
			event: "pull_request",
			scope: `{"quality":false,"unit":false,"database":false,"frontend":false,"native":false,"mobile":false}`,
			results: map[string]string{
				"quality": "skipped", "unit": "skipped", "database": "skipped", "frontend": "skipped",
				"native": "skipped", "mobile-compile": "skipped", "mobile": "skipped", "ios-dist": "skipped",
			},
			wantSuccess: true,
		},
		{
			name:  "frontend PR skips unrelated Go gates",
			event: "pull_request",
			scope: `{"quality":false,"unit":false,"database":false,"frontend":true,"native":true,"mobile":false}`,
			results: map[string]string{
				"quality": "skipped", "unit": "skipped", "database": "skipped",
				"mobile-compile": "skipped", "mobile": "skipped", "ios-dist": "skipped",
			},
			wantSuccess: true,
		},
		{
			name:  "race skip on a pull request",
			event: "pull_request",
			results: map[string]string{
				"unit": "skipped", "mobile": "skipped", "ios-dist": "skipped",
			},
			wantSuccess: false,
			wantOutput:  "unit=skipped",
		},
		{
			name:  "master push without PR jobs",
			event: "push",
			results: map[string]string{
				"unit": "skipped", "mobile-compile": "skipped", "ios-dist": "skipped",
			},
			wantSuccess: true,
		},
		{
			name:           "release without PR jobs",
			event:          "workflow_call",
			releaseVersion: "v1.2.3",
			results: map[string]string{
				"unit": "skipped", "mobile-compile": "skipped", "ios-dist": "skipped",
			},
			wantSuccess: true,
		},
		{
			name:           "release with iOS signing enabled skips nothing",
			event:          "workflow_call",
			iosSigning:     "true",
			releaseVersion: "v1.2.3",
			results:        map[string]string{},
			wantSuccess:    true,
		},
		{
			name:        "quality skip on a pull request",
			event:       "pull_request",
			results:     map[string]string{"quality": "skipped"},
			wantSuccess: false,
			wantOutput:  "quality=skipped",
		},
		{
			name:        "unit skip on a push",
			event:       "push",
			results:     map[string]string{"unit": "skipped"},
			wantSuccess: true,
		},
		{
			name:        "race skip on a push",
			event:       "push",
			results:     map[string]string{"unit": "skipped"},
			wantSuccess: true,
		},
		{
			name:        "production skip on a pull request",
			event:       "pull_request",
			results:     map[string]string{"frontend": "skipped"},
			wantSuccess: false,
			wantOutput:  "frontend=skipped",
		},
		{
			name:           "ios-dist skip on a release with signing enabled",
			event:          "push",
			releaseVersion: "v1.2.3",
			iosSigning:     "true",
			results:        map[string]string{"ios-dist": "skipped"},
			wantSuccess:    false,
			wantOutput:     "ios-dist=skipped",
		},
		{
			name:        "ios-dist skip on a push with signing disabled",
			event:       "push",
			results:     map[string]string{"ios-dist": "skipped"},
			wantSuccess: true,
		},
		{
			name:        "production failure",
			results:     map[string]string{"native": "failure"},
			wantSuccess: false,
			wantOutput:  "native=failure",
		},
		{
			name:        "compile failure",
			results:     map[string]string{"native": "failure"},
			wantSuccess: false,
			wantOutput:  "native=failure",
		},
		{
			name:        "quality cancellation",
			results:     map[string]string{"quality": "cancelled"},
			wantSuccess: false,
			wantOutput:  "quality=cancelled",
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			scope := scenario.scope
			if scope == "" {
				scope = aggregationScope(scenario.event)
			}
			results := make(map[string]map[string]string)
			for _, need := range aggregator.Needs {
				result := scenario.results[need]
				if result == "" {
					result = "success"
				}
				results[need] = map[string]string{"result": result}
			}
			payload, err := json.Marshal(results)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("node", "--input-type=module", "-e", script)
			command.Env = append(os.Environ(),
				"NEEDS="+string(payload),
				"EVENT_NAME="+scenario.event,
				"SCOPE="+scope,
				"IOS_SIGNING="+scenario.iosSigning,
				"RELEASE_VERSION="+scenario.releaseVersion,
			)
			output, err := command.CombinedOutput()
			if scenario.wantSuccess && err != nil {
				t.Fatalf("ci-ok rejected valid results: %v\n%s", err, output)
			}
			if !scenario.wantSuccess {
				if err == nil {
					t.Fatalf("ci-ok accepted unsuccessful results:\n%s", output)
				}
				if !strings.Contains(string(output), scenario.wantOutput) {
					t.Fatalf("ci-ok output does not contain %q:\n%s", scenario.wantOutput, output)
				}
			}
		})
	}
}

func TestPagesBuildsPullRequestsAndDeploysOnlyPush(t *testing.T) {
	pages := loadWorkflow(t, ".github/workflows/pages.yml")
	if pages.On.PullRequest == nil {
		t.Fatal("Pages does not run for pull requests")
	}
	if len(pages.On.Push.Branches) != 1 || pages.On.Push.Branches[0] != "master" {
		t.Fatalf("Pages push branches = %v", pages.On.Push.Branches)
	}
	for _, trigger := range []struct {
		name  string
		paths []string
	}{
		{name: "push", paths: pages.On.Push.Paths},
		{name: "pull request", paths: pages.On.PullRequest.Paths},
	} {
		found := false
		for _, path := range trigger.paths {
			found = found || path == "Taskfile.yml"
		}
		if !found {
			t.Errorf("Pages %s paths do not include Taskfile.yml", trigger.name)
		}
	}
	build := requireJob(t, pages, "build")
	if build.If != "" {
		t.Fatalf("Pages build unexpectedly limited by %q", build.If)
	}
	buildSite := requireRun(t, build, "pnpm exec tsc -p website/tsconfig.json --noEmit && pnpm exec vite build --config website/vite.config.mjs")
	install := requireRun(t, build, "pnpm install --frozen-lockfile")
	pnpm := requireUse(t, build, "pnpm/action-setup@")
	node := requireUse(t, build, "actions/setup-node@")
	requireBefore(t, build, pnpm, node, "pnpm must be installed before setup-node configures its cache")
	requireBefore(t, build, node, install, "Node must be installed before dependencies")
	requireBefore(t, build, install, buildSite, "dependencies must be installed before website:check")
	if node.With["cache"] != "pnpm" {
		t.Fatalf("Pages setup-node cache = %v", node.With["cache"])
	}

	var artifact *workflowStep
	for index := range build.Steps {
		if strings.HasPrefix(build.Steps[index].Uses, "actions/upload-pages-artifact@") {
			artifact = &build.Steps[index]
			break
		}
	}
	if artifact == nil || expression(artifact.If) != "github.event_name == 'push'" || artifact.With["path"] != "_site" {
		t.Fatalf("Pages artifact step = %#v", artifact)
	}
	deploy := requireJob(t, pages, "deploy")
	if expression(deploy.If) != "github.event_name == 'push'" || len(deploy.Needs) != 1 || deploy.Needs[0] != "build" {
		t.Fatalf("Pages deploy contract = if %q needs %v", deploy.If, deploy.Needs)
	}
	if deploy.Permissions["pages"] != "write" || deploy.Permissions["id-token"] != "write" {
		t.Fatalf("Pages deploy permissions = %v", deploy.Permissions)
	}

	tasks := loadTaskfile(t)
	commands := make(map[string]bool)
	for _, command := range tasks.Tasks["website:check"].Commands {
		commands[normalize(command.Run)] = true
	}
	for _, required := range []string{
		"pnpm exec tsc -p website/tsconfig.json --noEmit",
		"pnpm exec vite build --config website/vite.config.mjs",
	} {
		if !commands[required] {
			t.Errorf("website:check is missing %s", required)
		}
	}
}

func aggregationScope(event string) string {
	if event == "pull_request" {
		return `{"quality":true,"unit":true,"database":true,"frontend":true,"native":true,"mobile":true}`
	}
	return `{"quality":false,"unit":false,"database":false,"frontend":true,"native":true,"mobile":false}`
}

func TestHelperMatrixHasNineDirectTargetsAndThreeEvidenceRepresentatives(t *testing.T) {
	var entries []struct {
		Runner         string `json:"runner"`
		OS             string `json:"os"`
		Arch           string `json:"arch"`
		Kind           string `json:"kind"`
		Smoke          bool   `json:"smoke"`
		Cross          bool   `json:"cross"`
		Representative bool   `json:"representative"`
	}
	if err := json.Unmarshal([]byte(readRepositoryFile(t, ".github/native-matrix.json")), &entries); err != nil {
		t.Fatal(err)
	}
	helpers := make(map[string]bool)
	smoke := make(map[string]bool)
	cross := make(map[string]bool)
	representatives := make(map[string]bool)
	for _, entry := range entries {
		if entry.Kind != "helper" {
			continue
		}
		key := entry.OS + "/" + entry.Arch
		if helpers[key] {
			t.Errorf("duplicate helper target %s", key)
		}
		helpers[key] = true
		if entry.Smoke {
			smoke[key] = true
		}
		if entry.Cross {
			cross[key] = true
		}
		if entry.Representative {
			representatives[key] = true
		}
		if entry.Runner == "" {
			t.Errorf("helper target %s has no runner", key)
		}
	}
	for _, key := range []string{
		"linux/amd64", "linux/arm64", "linux/armv7", "linux/riscv64", "linux/loong64",
		"darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64",
	} {
		if !helpers[key] {
			t.Errorf("helper target %s is missing", key)
		}
		delete(helpers, key)
	}
	for key := range helpers {
		t.Errorf("unexpected helper target %s", key)
	}
	if len(smoke) != 1 || !smoke["linux/amd64"] {
		t.Errorf("ordinary PR helper smoke targets = %v, want only linux/amd64", smoke)
	}
	for _, key := range []string{"linux/armv7", "linux/riscv64", "linux/loong64"} {
		if !cross[key] {
			t.Errorf("hosted-runner cross target %s is not explicit", key)
		}
	}
	if len(representatives) != 3 || !representatives["linux/amd64"] || !representatives["windows/amd64"] || !representatives["darwin/arm64"] {
		t.Errorf("metadata representatives = %v, want linux/amd64, windows/amd64 and darwin/arm64", representatives)
	}

	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	version := requireJob(t, ci, "version")
	requireRun(t, version, "node .github/scripts/ci-plan.mjs")
	native := requireJob(t, ci, "native")
	build := requireRun(t, native, "node .github/scripts/build-native.mjs")
	if build.Env["NATIVE_TARGET"] != "${{ toJSON(matrix) }}" {
		t.Fatal("native build must consume the grouped target matrix")
	}

	release := loadWorkflow(t, ".github/workflows/release.yml")
	for _, jobName := range []string{"desktop", "server"} {
		job := requireJob(t, release, jobName)
		stage := requireRunContaining(t, job, ".github/scripts/helper-matrix.mjs --shell")
		if !strings.Contains(stage.Run, "while IFS='|' read -r helper_os helper_arch helper") {
			t.Errorf("%s does not stage the shared nine-helper list: %s", jobName, stage.Run)
		}
	}
}

func TestSetupNodeVersionsArePinnedAcrossCIReleaseAndPages(t *testing.T) {
	for _, name := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml", ".github/workflows/pages.yml"} {
		workflow := loadWorkflow(t, name)
		for jobName, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if !strings.HasPrefix(step.Uses, "actions/setup-node@") {
					continue
				}
				if got := step.With["node-version"]; got != "24.22.0" {
					t.Errorf("%s job %s uses node-version %v, want the pinned 24.22.0", name, jobName, got)
				}
			}
		}
	}
}

func TestReleaseWindowsToolingProvenanceFailsClosed(t *testing.T) {
	release := loadWorkflow(t, ".github/workflows/release.yml")
	desktop := requireJob(t, release, "desktop")
	nsis := requireRunContaining(t, desktop, "choco install nsis")
	if !strings.Contains(nsis.Run, "command -v makensis") {
		t.Error("NSIS install does not prefer the preinstalled makensis")
	}
	if !strings.Contains(nsis.Run, "--require-checksums") {
		t.Error("NSIS install does not require package checksums")
	}
	sevenZip := requireRunContaining(t, desktop, "choco install 7zip")
	if !strings.Contains(sevenZip.Run, "--require-checksums") {
		t.Error("7-Zip install does not require package checksums")
	}
}

func TestReleaseDesktopReusesEquivalentNativeEvidence(t *testing.T) {
	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	input, ok := ci.On.WorkflowCall.Inputs["updater_public_key"]
	if !ok || input.Required || input.Type != "string" {
		t.Fatalf("updater public key must be an optional non-secret string input: %#v", input)
	}
	if _, ok := ci.On.WorkflowCall.Secrets["NEXTERM_MINISIGN_PUBLIC_KEY"]; ok {
		t.Error("updater public key must not be routed as a secret")
	}
	native := requireJob(t, ci, "native")
	if got := native.Env["NEXTERM_MINISIGN_PUBLIC_KEY"]; got != "${{ inputs.updater_public_key }}" {
		t.Fatalf("native updater public key environment = %q", got)
	}
	if got := native.Env["NEXTERM_RELEASE_REF"]; got != "${{ github.ref }}" {
		t.Fatalf("native release ref environment = %q", got)
	}
	requireRun(t, native, "node .github/scripts/build-native.mjs")

	release := loadWorkflow(t, ".github/workflows/release.yml")
	caller := requireJob(t, release, "ci")
	if caller.Uses != "./.github/workflows/ci.yml" || caller.With["updater_public_key"] != "${{ vars.NEXTERM_MINISIGN_PUBLIC_KEY }}" {
		t.Fatalf("release native caller does not forward the public key: uses=%q with=%v", caller.Uses, caller.With)
	}
	desktop := requireJob(t, release, "desktop")
	if got := desktop.Env["NEXTERM_RELEASE_REF"]; got != "${{ github.ref }}" {
		t.Fatalf("desktop release ref environment = %q", got)
	}
	const nativeArtifact = "native-${{ matrix.os }}-${{ matrix.arch }}-desktop"
	downloadFound := false
	for _, step := range desktop.Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") && step.With["name"] == nativeArtifact {
			downloadFound = true
			if step.With["path"] != "target/release-work/reused-native" {
				t.Errorf("native desktop download path = %v", step.With["path"])
			}
		}
	}
	if !downloadFound {
		t.Error("desktop does not download its corresponding native binary and manifest")
	}
	stage := requireRunContaining(t, desktop, `binary="nexterm-desktop-`)
	for _, required := range []string{
		`cp "target/release-work/reused-native/go-build/${binary}" "target/release-work/reused-native/go-build/${binary}.manifest.json" target/go-build/`,
		".github/scripts/helper-matrix.mjs --shell",
		"while IFS='|' read -r helper_os helper_arch helper",
		`cp "target/release-work/verified-helpers/go-build/$helper"`,
	} {
		if !strings.Contains(stage.Run, required) {
			t.Errorf("desktop reuse staging is missing %q", required)
		}
	}
	packageStep := requireRunContaining(t, desktop, "node scripts/build.mjs desktop --release --package")
	for _, required := range []string{"--package-only", "--consume-dist=target/release-work/verified-dist/dist", "--dist-manifest=target/release-work/verified-dist/target/dist-manifest.json", "--require-evidence"} {
		if !strings.Contains(packageStep.Run, required) {
			t.Errorf("desktop package-only command is missing %q", required)
		}
	}
	if strings.Contains(packageStep.Run, "--repro-check") {
		t.Error("desktop package-only must leave repro-check to the native producing job")
	}
	requireRunContaining(t, desktop, "security import")
	requireRunContaining(t, desktop, "NEXTERM_MACOS_NOTARY_KEY_PATH")
	publish := requireJob(t, release, "publish")
	for _, need := range []string{"desktop", "server", "mobile", "agent"} {
		if !strings.Contains(expression(publish.If), "needs."+need+".result == 'success'") {
			t.Errorf("publish does not require %s success: %q", need, publish.If)
		}
	}
	requireRun(t, publish, "node .github/scripts/release-evidence.mjs target/release-work/candidate")
}

func TestCIDownloadsStayUnderTargetCIWork(t *testing.T) {
	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	downloads := 0
	for jobName, job := range ci.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/download-artifact@") {
				continue
			}
			downloads++
			downloadPath, _ := step.With["path"].(string)
			if !strings.HasPrefix(downloadPath, "target/ci-work/") {
				t.Errorf("%s downloads artifacts to %q, want a target/ci-work/ path", jobName, downloadPath)
			}
		}
	}
	if downloads == 0 {
		t.Fatal("no CI artifact downloads found")
	}
}

func TestExistingCISecurityAndBackendContractsRemain(t *testing.T) {
	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	database := requireJob(t, ci, "database")
	if database.Env["NEXTERM_TEST_DB_BACKEND"] != "${{ matrix.backend }}" || database.Env["NEXTERM_TEST_DB_DSN"] != "${{ matrix.dsn }}" {
		t.Fatalf("database environment = %v", database.Env)
	}
	backends := make(map[string]bool)
	digest := regexp.MustCompile(`^[^@]+@sha256:[0-9a-f]{64}$`)
	for _, entry := range database.Strategy.Matrix.Include {
		backend, _ := entry["backend"].(string)
		backends[backend] = true
		if backend != "sqlite" {
			image, _ := entry["image"].(string)
			if !digest.MatchString(image) {
				t.Errorf("%s image is not digest pinned: %q", backend, image)
			}
		}
	}
	for _, backend := range []string{"postgres", "mysql", "mariadb", "sqlite"} {
		if !backends[backend] {
			t.Errorf("database backend %s is missing", backend)
		}
	}
	requireRunContaining(t, database, "task test:database")

	native := requireJob(t, ci, "native")
	windowsTests := requireRunContaining(t, native, "& ./.github/scripts/windows-real-tests.ps1")
	if !strings.Contains(windowsTests.If, "matrix.os == 'windows'") {
		t.Fatalf("Windows real tests condition = %q", windowsTests.If)
	}
	windowsScript := readRepositoryFile(t, ".github/scripts/windows-real-tests.ps1")
	for _, testName := range []string{"TestSessionWindowsNaturalExitEOF", "TestSessionWindowsBackpressureAndDrain"} {
		if !strings.Contains(windowsScript, "-Package './internal/pty' -Test '"+testName+"'") {
			t.Errorf("Windows PTY test %s is not wired", testName)
		}
	}

	for _, secret := range []string{
		"NEXTERM_ANDROID_KEYSTORE_BASE64",
		"ANDROID_KEYSTORE_PASSWORD",
		"ANDROID_KEY_ALIAS",
		"ANDROID_KEY_PASSWORD",
		"NEXTERM_IOS_P12_BASE64",
		"NEXTERM_IOS_P12_PASSWORD",
		"NEXTERM_IOS_PROVISIONING_PROFILE",
	} {
		if _, ok := ci.On.WorkflowCall.Secrets[secret]; !ok {
			t.Errorf("workflow_call secret %s is missing", secret)
		}
	}
	mobile := requireJob(t, ci, "mobile")
	packageStep := requireRun(t, mobile, "wails3 task ${{ matrix.platform }}:package")
	for _, secret := range []string{"ANDROID_KEYSTORE_PASSWORD", "ANDROID_KEY_ALIAS", "ANDROID_KEY_PASSWORD"} {
		if !strings.Contains(packageStep.Env[secret], "secrets."+secret) {
			t.Errorf("mobile package secret %s is not scoped to the package step", secret)
		}
	}

	uploads := 0
	for _, job := range ci.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				continue
			}
			uploads++
			if overwrite, _ := step.With["overwrite"].(bool); !overwrite {
				t.Errorf("%s upload does not overwrite reruns", job.Name)
			}
			if step.With["if-no-files-found"] != "error" {
				t.Errorf("%s upload does not reject missing files", job.Name)
			}
		}
	}
	if uploads == 0 {
		t.Fatal("no CI artifact uploads found")
	}
}

func loadWorkflow(t *testing.T, name string) workflowFile {
	t.Helper()
	var workflow workflowFile
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, name)), &workflow); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return workflow
}

func loadTaskfile(t *testing.T) taskFile {
	t.Helper()
	return loadTaskfileAt(t, "Taskfile.yml")
}

func loadTaskfileAt(t *testing.T, name string) taskFile {
	t.Helper()
	var tasks taskFile
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, name)), &tasks); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return tasks
}

func readRepositoryFile(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate workflow contract source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func requireJob(t *testing.T, workflow workflowFile, name string) workflowJob {
	t.Helper()
	job, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("job %s not found", name)
	}
	return job
}

func requireUse(t *testing.T, job workflowJob, prefix string) workflowStep {
	t.Helper()
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, prefix) {
			return step
		}
	}
	t.Fatalf("%s has no step using %s", job.Name, prefix)
	return workflowStep{}
}

func requireRun(t *testing.T, job workflowJob, command string) workflowStep {
	t.Helper()
	for _, step := range job.Steps {
		if normalize(step.Run) == command {
			return step
		}
	}
	t.Fatalf("%s has no step running %q", job.Name, command)
	return workflowStep{}
}

func requireRunContaining(t *testing.T, job workflowJob, command string) workflowStep {
	t.Helper()
	for _, step := range job.Steps {
		if strings.Contains(normalize(step.Run), command) {
			return step
		}
	}
	t.Fatalf("%s has no step containing %q", job.Name, command)
	return workflowStep{}
}

func requireBefore(t *testing.T, job workflowJob, first, second workflowStep, message string) {
	t.Helper()
	firstIndex, secondIndex := -1, -1
	for index, step := range job.Steps {
		if step.ID == first.ID && step.Name == first.Name && step.If == first.If && step.Uses == first.Uses && step.Run == first.Run {
			firstIndex = index
		}
		if step.ID == second.ID && step.Name == second.Name && step.If == second.If && step.Uses == second.Uses && step.Run == second.Run {
			secondIndex = index
		}
	}
	if firstIndex < 0 || secondIndex < 0 || firstIndex >= secondIndex {
		t.Fatalf("%s: %s", job.Name, message)
	}
}

func extractNodeScript(t *testing.T, run string) string {
	t.Helper()
	const prefix = "node --input-type=module -e '"
	start := strings.Index(run, prefix)
	if start < 0 {
		t.Fatalf("Node aggregation command not found in %q", run)
	}
	script := strings.TrimSpace(run[start+len(prefix):])
	if !strings.HasSuffix(script, "'") {
		t.Fatalf("unterminated Node aggregation script in %q", run)
	}
	return strings.TrimSpace(strings.TrimSuffix(script, "'"))
}

func expression(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "${{")
	value = strings.TrimSuffix(value, "}}")
	return strings.TrimSpace(value)
}

func normalize(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
