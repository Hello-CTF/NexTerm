package ciworkflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The mobile jobs never stamp the tracked build/config.yml: the version flows
// through the Taskfile dependency chain into the ignored generated stamp
// config (scripts/mobile-source.mjs), keeping the release tree clean so the
// embedded commit label matches the frontend dist manifest.
func TestMobileJobsRelyOnTheIgnoredSelfStamp(t *testing.T) {
	ci := loadWorkflow(t, ".github/workflows/ci.yml")
	for _, jobName := range []string{"mobile", "ios-dist"} {
		job := requireJob(t, ci, jobName)
		for _, step := range job.Steps {
			run := normalize(step.Run)
			if strings.Contains(run, "stamp-version.mjs") {
				t.Errorf("%s stamps the tracked mobile config: %s", jobName, run)
			}
		}
	}
	requireRun(t, requireJob(t, ci, "mobile"), "wails3 task ${{ matrix.platform }}:package")
	requireRun(t, requireJob(t, ci, "ios-dist"), "wails3 task ios:package:ipa IOS_PLATFORM=device")

	common := loadTaskfileAt(t, "build/Taskfile.yml")
	stamp := common.Tasks["stamp:mobile:config"]
	stamps := false
	for _, command := range stamp.Commands {
		if strings.Contains(command.Run, "mobile-source.mjs stamp") {
			stamps = true
		}
	}
	if !stamps {
		t.Error("build/Taskfile.yml stamp:mobile:config does not run `node scripts/mobile-source.mjs stamp`")
	}

	android := loadTaskfileAt(t, "build/android/Taskfile.yml")
	requireTaskDep(t, android, "generate:android:overlay", "common:stamp:mobile:config")
	requireTaskDep(t, android, "build", "generate:android:overlay")
	requireTaskDep(t, android, "package", "build")

	ios := loadTaskfileAt(t, "build/ios/Taskfile.yml")
	requireTaskDep(t, ios, "generate:ios:overlay", "common:stamp:mobile:config")
	requireTaskDep(t, ios, "generate:ios:xcode", "common:stamp:mobile:config")
	requireTaskDep(t, ios, "build", "generate:ios:overlay")
	requireTaskDep(t, ios, "package", "build")
	requireTaskDep(t, ios, "package:ipa", "package")
}

func requireTaskDep(t *testing.T, tasks taskFile, name, dep string) {
	t.Helper()
	task, ok := tasks.Tasks[name]
	if !ok {
		t.Fatalf("task %s not found", name)
	}
	for _, entry := range task.Deps {
		if entry.Task == dep {
			return
		}
	}
	t.Errorf("task %s is missing the %s dependency", name, dep)
}

// The exact stamp→consume commands the CI jobs rely on, run against the real
// tracked config: stamping writes the ignored generated config, `config`
// consumes it, unsetting the version removes the stale stamp and falls back to
// the tracked 0.0.0 placeholder config.
func TestMobileSourceStampConsumesAndFallsBackOnTheRealConfig(t *testing.T) {
	root := repositoryRoot(t)
	stamp := filepath.Join(root, "build", "config.stamp.yml")
	tracked := filepath.Join(root, "build", "config.yml")

	priorStamp, priorErr := os.ReadFile(stamp)
	hadStamp := priorErr == nil
	t.Cleanup(func() {
		if hadStamp {
			_ = os.WriteFile(stamp, priorStamp, 0o644)
		} else {
			_ = os.Remove(stamp)
		}
	})

	before, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	run := func(version string, args ...string) string {
		t.Helper()
		command := exec.Command("node", append([]string{filepath.Join(root, "scripts", "mobile-source.mjs")}, args...)...)
		command.Dir = root
		command.Env = append(os.Environ(), "NEXTERM_RELEASE_VERSION="+version)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("mobile-source.mjs %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}

	if got := run("1.2.3", "stamp"); !strings.Contains(got, "tracked build/config.yml is untouched") {
		t.Fatalf("stamp output does not confirm the tracked config is untouched: %q", got)
	}
	stamped, err := os.ReadFile(stamp)
	if err != nil {
		t.Fatalf("stamp did not write the ignored generated config: %v", err)
	}
	if !strings.Contains(string(stamped), `version: "1.2.3"`) {
		t.Fatalf("stamped config does not carry the release version:\n%s", stamped)
	}
	after, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("stamping modified the tracked build/config.yml")
	}
	if got := run("1.2.3", "config"); got != "build/config.stamp.yml" {
		t.Fatalf("config did not consume the generated stamp: %q", got)
	}

	run("", "stamp")
	if _, err := os.Stat(stamp); !os.IsNotExist(err) {
		t.Fatalf("unsetting the version did not remove the stale stamp: %v", err)
	}
	if got := run("", "config"); got != "build/config.yml" {
		t.Fatalf("config did not fall back to the tracked config: %q", got)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
