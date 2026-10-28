package agent

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/version"
)

type updateHarness struct {
	replaced []stagedReplace
	target   string
	release  *httptest.Server
	latest   string
	hits     atomic.Int32
}

// newUpdateHarness serves a fake "<base>/download/v<ver>/..." tree plus "<base>/latest" redirect, and wires every
// update test seam so nothing touches the network or the real executable.
func newUpdateHarness(t *testing.T, current, latest string, stdin string, terminal bool) *updateHarness {
	t.Helper()
	h := &updateHarness{latest: latest}
	files := map[string][]byte{}
	for _, v := range []string{"1.2.0", "1.3.0", "1.4.0"} {
		asset := releaseAssetName(v, runtime.GOOS, runtime.GOARCH)
		archive := buildReleaseArchive(t, runtime.GOOS, []byte("agent-"+v))
		files["/download/v"+v+"/"+asset] = archive
		files["/download/v"+v+"/"+checksumsFileName] = []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset))
	}
	h.release = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		if r.URL.Path == "/latest" {
			http.Redirect(w, r, "/tag/v"+h.latest, http.StatusFound)
			return
		}
		content, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	}))
	t.Cleanup(h.release.Close)
	h.target = filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	if err := os.WriteFile(h.target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	restoreVersion := version.Version
	version.Version = current
	restoreFactory, restoreExe, restoreStdin, restoreTTY := cliUpgraderFactory, cliExecutable, cliStdin, cliStdinIsTerminal
	cliUpgraderFactory = func(string) *Upgrader {
		return &Upgrader{
			BaseURL: h.release.URL + "/download", Executable: h.target,
			MaxAttempts: 1, Backoff: time.Millisecond,
			sleep: instantSleep, scheduleReplace: captureReplace(&h.replaced),
		}
	}
	cliExecutable = func() (string, error) { return h.target, nil }
	cliStdin = strings.NewReader(stdin)
	cliStdinIsTerminal = func() bool { return terminal }
	t.Cleanup(func() {
		version.Version = restoreVersion
		cliUpgraderFactory, cliExecutable, cliStdin, cliStdinIsTerminal = restoreFactory, restoreExe, restoreStdin, restoreTTY
	})
	stubCLIManager(t)
	return h
}

func runUpdate(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := RunCLI(append([]string{"update"}, args...), func(string) string { return "" }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUpdateCheckDoesNotDownloadAndExitsZero(t *testing.T) {
	h := newUpdateHarness(t, "1.3.0", "1.4.0", "", false)
	code, out, errOut := runUpdate("--check")
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "1.3.0") || !strings.Contains(out, "1.4.0") {
		t.Fatalf("stdout=%q", out)
	}
	if len(h.replaced) != 0 {
		t.Fatalf("--check staged a replacement: %+v", h.replaced)
	}
}

func TestUpdateSameVersionUpToDateUnlessForced(t *testing.T) {
	h := newUpdateHarness(t, "1.4.0", "1.4.0", "", false)
	if code, _, errOut := runUpdate(); code != ExitOK || len(h.replaced) != 0 {
		t.Fatalf("same version: code=%d replaced=%d stderr=%q", code, len(h.replaced), errOut)
	}
	if code, _, errOut := runUpdate("--force", "--yes"); code != ExitOK || len(h.replaced) != 1 {
		t.Fatalf("forced reinstall: code=%d replaced=%d stderr=%q", code, len(h.replaced), errOut)
	}
}

func TestUpdateNonInteractiveRequiresYes(t *testing.T) {
	h := newUpdateHarness(t, "1.3.0", "1.4.0", "", false)
	code, _, errOut := runUpdate()
	if code != ExitUsage || len(h.replaced) != 0 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("code=%d replaced=%d stderr=%q", code, len(h.replaced), errOut)
	}
	if code, _, errOut := runUpdate("-y"); code != ExitOK || len(h.replaced) != 1 {
		t.Fatalf("-y: code=%d replaced=%d stderr=%q", code, len(h.replaced), errOut)
	}
}

func TestUpdateInteractiveConfirmation(t *testing.T) {
	declined := newUpdateHarness(t, "1.3.0", "1.4.0", "n\n", true)
	if code, _, _ := runUpdate(); code == ExitOK || len(declined.replaced) != 0 {
		t.Fatalf("declined: code=%d replaced=%d", code, len(declined.replaced))
	}
	accepted := newUpdateHarness(t, "1.3.0", "1.4.0", "yes\n", true)
	code, out, errOut := runUpdate()
	if code != ExitOK || len(accepted.replaced) != 1 || !strings.Contains(out, "1.3.0 -> 1.4.0") {
		t.Fatalf("accepted: code=%d replaced=%d out=%q stderr=%q", code, len(accepted.replaced), out, errOut)
	}
	if accepted.replaced[0].targetPath != accepted.target {
		t.Fatalf("replace target = %q", accepted.replaced[0].targetPath)
	}
}

func TestUpdatePinnedVersionAndDowngrade(t *testing.T) {
	h := newUpdateHarness(t, "1.3.0", "1.4.0", "", false)
	if code, _, _ := runUpdate("--version", "1.2.0", "--yes"); code != ExitUsage || len(h.replaced) != 0 {
		t.Fatalf("downgrade without --force: code=%d replaced=%d", code, len(h.replaced))
	}
	if code, _, errOut := runUpdate("--version", "v1.2.0", "-f", "-y"); code != ExitOK || len(h.replaced) != 1 {
		t.Fatalf("forced downgrade: code=%d replaced=%d stderr=%q", code, len(h.replaced), errOut)
	}
	if h.hits.Load() == 0 {
		t.Fatal("expected release requests")
	}
}

func TestUpdateLatestOlderThanCurrentIsUpToDate(t *testing.T) {
	h := newUpdateHarness(t, "1.5.0", "1.4.0", "", false)
	if code, _, _ := runUpdate("--yes"); code != ExitOK || len(h.replaced) != 0 {
		t.Fatalf("code=%d replaced=%d", code, len(h.replaced))
	}
}

func TestUpdateDevBuildNeedsForce(t *testing.T) {
	h := newUpdateHarness(t, "dev", "1.4.0", "", false)
	if code, _, _ := runUpdate("--yes"); code != ExitUsage || len(h.replaced) != 0 {
		t.Fatalf("code=%d replaced=%d", code, len(h.replaced))
	}
	if code, _, errOut := runUpdate("--yes", "--force"); code != ExitOK || len(h.replaced) != 1 {
		t.Fatalf("code=%d replaced=%d stderr=%q", code, len(h.replaced), errOut)
	}
}

func TestUpdateFailuresKeepBinaryAndExitNonZero(t *testing.T) {
	h := newUpdateHarness(t, "1.3.0", "1.4.0", "", false)
	// 1.9.9 has no assets on the fake release source.
	if code, _, _ := runUpdate("--version", "1.9.9", "-y"); code == ExitOK || len(h.replaced) != 0 {
		t.Fatalf("missing release: code=%d replaced=%d", code, len(h.replaced))
	}
	if data, _ := os.ReadFile(h.target); string(data) != "old-binary" {
		t.Fatalf("binary modified: %q", data)
	}
	h.latest = "not a version!"
	if code, _, _ := runUpdate("--check"); code == ExitOK {
		t.Fatal("garbage latest tag must fail")
	}
	if code, _, _ := runUpdate("--version", "../evil", "-y"); code != ExitUsage {
		t.Fatalf("invalid version code=%d", code)
	}
}

func TestUpdateAliasAndEmbeddedRefusal(t *testing.T) {
	newUpdateHarness(t, "1.3.0", "1.4.0", "", false)
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"upgrade", "--check"}, nil, &stdout, &stderr); code != ExitOK {
		t.Fatalf("alias code=%d %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunCLIWithServicePrefix([]string{"update", "-y"}, []string{"agent"}, nil, &stdout, &stderr); code != ExitError {
		t.Fatalf("embedded code=%d", code)
	}
}

func TestResolveLatestVersionParsesRedirect(t *testing.T) {
	if got := latestReleaseURL(ReleaseBaseURL); got != "https://github.com/Hello-CTF/NexTerm/releases/latest" {
		t.Fatalf("latest url = %q", got)
	}
	cases := map[string]struct {
		cmp int
		ok  bool
	}{"1.3.0": {-1, true}, "1.2.0-rc.1": {1, true}, "dev": {0, false}}
	for target, want := range cases {
		cmp, ok := compareReleaseVersions("1.2.5", target)
		if ok != want.ok || (ok && cmp != want.cmp) {
			t.Errorf("compare(1.2.5,%s)=%d,%v", target, cmp, ok)
		}
	}
}
