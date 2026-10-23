//go:build !windows

// Package installer holds acceptance tests for the shell installers. Every
// external effect goes through stubs on PATH: no network, no services and no
// real secrets.
package installer

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	deviceVersion = "9.9.9-test"
	releaseBase   = "https://github.com/Hello-CTF/NexTerm/releases/download"
	serverURL     = "https://nexterm.example.com"
	enrollCode    = "TEST-CODE-SECRET-123"
)

// fakeAgent records its argv and simulates enroll/install outcomes.
const fakeAgent = `#!/bin/sh
{
  printf '==\n'
  for arg in "$@"; do printf '%s\n' "$arg"; done
} >> "$FAKE_AGENT_LOG"
if [ "${1:-}" = "enroll" ]; then
  if [ "${FAKE_ENROLL_FAIL:-}" = "1" ]; then
    printf 'agent enroll: stub 注册失败\n' >&2
    exit 1
  fi
  printf '注册成功: device_id=dev-stub name=stub base_urls=1 metrics_interval=30s desired_autostart=true terminal=开启\n'
  exit 0
fi
if [ "${1:-}" = "install" ]; then
  if [ "${FAKE_INSTALL_FAIL:-}" = "1" ]; then
    printf 'agent install: stub 安装失败\n' >&2
    exit 1
  fi
  printf 'install 完成: installed=true enabled=true active=true\n'
  exit 0
fi
exit 0
`

const fakeCurl = `#!/bin/sh
dest=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) dest="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\n' "$url" >> "$FAKE_CURL_LOG"
base="${url##*/}"
if [ -f "$FAKE_RELEASE_DIR/$base" ]; then
  cp "$FAKE_RELEASE_DIR/$base" "$dest"
  exit 0
fi
printf 'fake curl: HTTP 404 %s\n' "$url" >&2
exit 22
`

const fakeWget = `#!/bin/sh
dest=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -O) dest="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\n' "$url" >> "$FAKE_CURL_LOG"
base="${url##*/}"
if [ -f "$FAKE_RELEASE_DIR/$base" ]; then
  cp "$FAKE_RELEASE_DIR/$base" "$dest"
  exit 0
fi
printf 'fake wget: 404 %s\n' "$url" >&2
exit 8
`

const fakeID = `#!/bin/sh
if [ "${FAKE_UID:-}" != "" ] && [ "${1:-}" = "-u" ]; then
  printf '%s\n' "$FAKE_UID"
  exit 0
fi
exec @REAL@ "$@"
`

const fakeUname = `#!/bin/sh
case "${1:-}" in
  -s) printf '%s\n' "${FAKE_UNAME_S:-Linux}" ;;
  -m) printf '%s\n' "${FAKE_UNAME_M:-x86_64}" ;;
  *) exec @REAL@ "$@" ;;
esac
`

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func lookPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("acceptance requires system tool %s", name)
	}
	return path
}

func agentAsset(osName, arch string) string {
	if osName == "darwin" {
		osName = "macos"
	}
	return fmt.Sprintf("nexterm-agent_%s_%s_%s.tar.gz", osName, arch, deviceVersion)
}

var allAgentAssets = [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}}

type deviceOptions struct {
	noCurl, wget, noSHA256Sum, badLayout bool
	sums                                 func(hashes map[string]string) string
}

type deviceHarness struct {
	t                              *testing.T
	stub, home, release, work, tmp string
	curlLog, agentLog              string
}

func newDeviceHarness(t *testing.T, options deviceOptions) *deviceHarness {
	t.Helper()
	base := t.TempDir()
	h := &deviceHarness{
		t: t, stub: filepath.Join(base, "stub"), home: filepath.Join(base, "home"), release: filepath.Join(base, "release"),
		work: filepath.Join(base, "work"), tmp: filepath.Join(base, "tmp"),
		curlLog: filepath.Join(base, "curl.log"), agentLog: filepath.Join(base, "agent.log"),
	}
	for _, dir := range []string{h.stub, h.home, h.release, h.work, h.tmp} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tools := []string{"mktemp", "awk", "tar", "gzip", "install", "mkdir", "rm", "cp"}
	if !options.noSHA256Sum {
		tools = append(tools, "sha256sum")
	}
	for _, name := range tools {
		h.writeStub(name, fmt.Sprintf("#!/bin/sh\nexec %s \"$@\"\n", lookPath(t, name)))
	}
	if options.noSHA256Sum {
		h.writeStub("shasum", fmt.Sprintf("#!/bin/sh\n[ \"${1:-}\" = \"-a\" ] && shift 2\nexec %s \"$@\"\n", lookPath(t, "sha256sum")))
	}
	h.writeStub("id", strings.ReplaceAll(fakeID, "@REAL@", lookPath(t, "id")))
	h.writeStub("uname", strings.ReplaceAll(fakeUname, "@REAL@", lookPath(t, "uname")))
	if !options.noCurl {
		h.writeStub("curl", fakeCurl)
	}
	if options.wget {
		h.writeStub("wget", fakeWget)
	}
	h.writeRelease(options)
	return h
}

func (h *deviceHarness) writeStub(name, content string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.stub, name), []byte(content), 0o755); err != nil {
		h.t.Fatal(err)
	}
}

func (h *deviceHarness) writeRelease(options deviceOptions) {
	h.t.Helper()
	hashes := map[string]string{}
	for _, asset := range allAgentAssets {
		name := agentAsset(asset[0], asset[1])
		// The tar root must hold only nexterm-agent; badLayout simulates the
		// old nested-directory layout, which the installer must refuse.
		member := "nexterm-agent"
		if options.badLayout && asset == [2]string{"linux", "amd64"} {
			member = fmt.Sprintf("NexTerm-agent_%s_linux_amd64/nexterm-agent", deviceVersion)
		}
		path := filepath.Join(h.release, name)
		writeTarGz(h.t, path, member, []byte(fakeAgent))
		data, err := os.ReadFile(path)
		if err != nil {
			h.t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	var sums string
	if options.sums != nil {
		sums = options.sums(hashes)
	} else {
		for _, asset := range allAgentAssets {
			name := agentAsset(asset[0], asset[1])
			sums += hashes[name] + "  " + name + "\n"
		}
		sums += strings.Repeat("0", 64) + "  nexterm-desktop_linux_amd64_" + deviceVersion + ".deb\n"
	}
	if err := os.WriteFile(filepath.Join(h.release, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func writeTarGz(t *testing.T, path, member string, data []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed := gzip.NewWriter(file)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: member, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	writer.Write(data)
	writer.Close()
	compressed.Close()
}

type runResult struct {
	code           int
	stdout, stderr string
}

func (h *deviceHarness) run(env map[string]string, args ...string) runResult {
	h.t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(repoRoot(h.t), "public", "install-device.sh")}, args...)...)
	cmd.Dir = h.work
	cmd.Env = []string{
		"PATH=" + h.stub, "HOME=" + h.home, "TMPDIR=" + h.tmp, "FAKE_UID=1000",
		"FAKE_RELEASE_DIR=" + h.release, "FAKE_CURL_LOG=" + h.curlLog, "FAKE_AGENT_LOG=" + h.agentLog,
	}
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	return runCommand(h.t, cmd)
}

func runCommand(t *testing.T, cmd *exec.Cmd) runResult {
	t.Helper()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return runResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func baseArgs(version string) []string {
	return []string{"--server", serverURL, "--code", enrollCode, "--version", version}
}

func (h *deviceHarness) urls() []string {
	data, err := os.ReadFile(h.curlLog)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func (h *deviceHarness) calls() [][]string {
	data, err := os.ReadFile(h.agentLog)
	if err != nil {
		return nil
	}
	var records [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "==" {
			records = append(records, []string{})
		} else if len(records) > 0 {
			records[len(records)-1] = append(records[len(records)-1], line)
		}
	}
	return records
}

func (h *deviceHarness) installedBinary() string {
	return filepath.Join(h.home, ".local", "bin", "nexterm-agent")
}

func (h *deviceHarness) defaultDataDir() string {
	return h.home + "/.local/share/NexTerm"
}

func expectedCalls(dataDir string, insecure bool) [][]string {
	enroll := []string{"enroll", "--server", serverURL, "--code", enrollCode}
	if insecure {
		enroll = append(enroll, "--insecure")
	}
	enroll = append(enroll, "--data-dir", dataDir)
	return [][]string{enroll, {"install", "--data-dir", dataDir}}
}

func (h *deviceHarness) expectNothingInstalled() {
	h.t.Helper()
	if calls := h.calls(); len(calls) != 0 {
		h.t.Fatalf("agent was invoked: %v", calls)
	}
	if _, err := os.Stat(h.installedBinary()); err == nil {
		h.t.Fatal("binary was installed")
	}
}

func expectFailure(t *testing.T, got runResult, code int, message string) {
	t.Helper()
	if got.code != code || !strings.Contains(got.stderr, message) {
		t.Fatalf("exit=%d stderr=%q; want exit %d with %q", got.code, got.stderr, code, message)
	}
}

func TestDeviceHelp(t *testing.T) {
	got := newDeviceHarness(t, deviceOptions{}).run(nil, "--help")
	if got.code != 0 || !strings.Contains(got.stdout, "用法") {
		t.Fatalf("%+v", got)
	}
}

func TestDeviceArgumentErrors(t *testing.T) {
	h := newDeviceHarness(t, deviceOptions{})
	for flag, args := range map[string][]string{
		"--server":  {"--code", enrollCode, "--version", deviceVersion},
		"--code":    {"--server", serverURL, "--version", deviceVersion},
		"--version": {"--server", serverURL, "--code", enrollCode},
	} {
		expectFailure(t, h.run(nil, args...), 2, flag)
	}
	expectFailure(t, h.run(nil, append(baseArgs(deviceVersion), "--bogus")...), 2, "未知参数")
	if urls := h.urls(); len(urls) != 0 {
		t.Fatalf("argument errors downloaded %v", urls)
	}
}

func TestDevicePlatformRefusals(t *testing.T) {
	for _, test := range []struct {
		env     map[string]string
		message string
	}{
		{map[string]string{"FAKE_UID": "0"}, "root"},
		{map[string]string{"FAKE_UNAME_S": "FreeBSD"}, "仅支持 Linux/macOS"},
		{map[string]string{"FAKE_UNAME_M": "mips64"}, "不支持的 CPU 架构"},
	} {
		h := newDeviceHarness(t, deviceOptions{})
		expectFailure(t, h.run(test.env, baseArgs(deviceVersion)...), 1, test.message)
		if urls := h.urls(); len(urls) != 0 {
			t.Fatalf("%s downloaded %v", test.message, urls)
		}
	}
	expectFailure(t, newDeviceHarness(t, deviceOptions{noCurl: true}).run(nil, baseArgs(deviceVersion)...), 1, "curl 或 wget")
}

func TestDeviceWgetFallback(t *testing.T) {
	h := newDeviceHarness(t, deviceOptions{noCurl: true, wget: true})
	got := h.run(nil, baseArgs(deviceVersion)...)
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	want := []string{releaseBase + "/v" + deviceVersion + "/" + agentAsset("linux", "amd64"), releaseBase + "/v" + deviceVersion + "/SHA256SUMS"}
	if !reflect.DeepEqual(h.urls(), want) {
		t.Fatalf("urls = %v", h.urls())
	}
}

func TestDeviceShasumFallback(t *testing.T) {
	h := newDeviceHarness(t, deviceOptions{noSHA256Sum: true})
	got := h.run(nil, baseArgs(deviceVersion)...)
	if got.code != 0 || !reflect.DeepEqual(h.calls(), expectedCalls(h.defaultDataDir(), false)) {
		t.Fatalf("%+v calls=%v", got, h.calls())
	}
}

func TestDeviceDownloadAndChecksumFailures(t *testing.T) {
	h := newDeviceHarness(t, deviceOptions{})
	expectFailure(t, h.run(nil, baseArgs("8.8.8-missing")...), 1, "下载失败")
	h.expectNothingInstalled()

	for message, sums := range map[string]func(map[string]string) string{
		"校验失败": func(map[string]string) string {
			return strings.Repeat("a", 64) + "  " + agentAsset("linux", "amd64") + "\n"
		},
		"SHA256SUMS": func(map[string]string) string {
			return strings.Repeat("a", 64) + "  nexterm-desktop_linux_amd64_" + deviceVersion + ".deb\n"
		},
		"不是合法 sha256": func(map[string]string) string {
			return strings.Repeat("z", 64) + "  " + agentAsset("linux", "amd64") + "\n"
		},
	} {
		h := newDeviceHarness(t, deviceOptions{sums: sums})
		expectFailure(t, h.run(nil, baseArgs(deviceVersion)...), 1, message)
		h.expectNothingInstalled()
	}

	h = newDeviceHarness(t, deviceOptions{badLayout: true})
	expectFailure(t, h.run(nil, baseArgs(deviceVersion)...), 1, "缺少 nexterm-agent")
	h.expectNothingInstalled()
}

func expectDeviceSuccess(t *testing.T, h *deviceHarness, got runResult, osName, arch string) {
	t.Helper()
	if got.code != 0 {
		t.Fatalf("%s/%s failed: %+v", osName, arch, got)
	}
	want := []string{releaseBase + "/v" + deviceVersion + "/" + agentAsset(osName, arch), releaseBase + "/v" + deviceVersion + "/SHA256SUMS"}
	if !reflect.DeepEqual(h.urls(), want) {
		t.Fatalf("urls = %v, want %v", h.urls(), want)
	}
	data, err := os.ReadFile(h.installedBinary())
	if err != nil || string(data) != fakeAgent {
		t.Fatalf("installed binary = %q, %v", data, err)
	}
	if info, _ := os.Stat(h.installedBinary()); info.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode = %o", info.Mode().Perm())
	}
	if !reflect.DeepEqual(h.calls(), expectedCalls(h.defaultDataDir(), false)) {
		t.Fatalf("calls = %v", h.calls())
	}
	if strings.Contains(got.stdout+got.stderr, enrollCode) || !strings.Contains(got.stdout, "--code ********") {
		t.Fatalf("enrollment code was not masked: %s", got.stdout+got.stderr)
	}
}

func TestDeviceInstallsOnEveryPlatform(t *testing.T) {
	h := newDeviceHarness(t, deviceOptions{})
	got := h.run(nil, baseArgs(deviceVersion)...)
	expectDeviceSuccess(t, h, got, "linux", "amd64")
	if !strings.Contains(got.stdout, "systemd --user") {
		t.Fatalf("linux output = %s", got.stdout)
	}

	// The arm64 sum must be selected exactly even when the amd64 line is wrong.
	h = newDeviceHarness(t, deviceOptions{sums: func(hashes map[string]string) string {
		arm64 := agentAsset("linux", "arm64")
		return hashes[arm64] + "  " + arm64 + "\n" + strings.Repeat("b", 64) + "  " + agentAsset("linux", "amd64") + "\n"
	}})
	expectDeviceSuccess(t, h, h.run(map[string]string{"FAKE_UNAME_M": "aarch64"}, baseArgs(deviceVersion)...), "linux", "arm64")

	h = newDeviceHarness(t, deviceOptions{})
	got = h.run(map[string]string{"FAKE_UNAME_S": "Darwin", "FAKE_UNAME_M": "arm64"}, baseArgs(deviceVersion)...)
	expectDeviceSuccess(t, h, got, "darwin", "arm64")
	if !strings.Contains(got.stdout, "launchd") {
		t.Fatalf("darwin output = %s", got.stdout)
	}

	h = newDeviceHarness(t, deviceOptions{})
	expectDeviceSuccess(t, h, h.run(map[string]string{"FAKE_UNAME_S": "Darwin", "FAKE_UNAME_M": "x86_64"}, baseArgs(deviceVersion)...), "darwin", "amd64")
}

func TestDeviceInsecureAndDataDirectories(t *testing.T) {
	h := newDeviceHarness(t, deviceOptions{})
	got := h.run(nil, append(baseArgs(deviceVersion), "--insecure")...)
	if got.code != 0 || !reflect.DeepEqual(h.calls(), expectedCalls(h.defaultDataDir(), true)) {
		t.Fatalf("--insecure: %+v %v", got, h.calls())
	}
	for _, test := range []struct {
		env  map[string]string
		args []string
		want string
	}{
		// A value with spaces must stay one argv entry.
		{map[string]string{"NEXTERM_DATA_DIR": "/tmp/fake data dir"}, nil, "/tmp/fake data dir"},
		{map[string]string{"XDG_DATA_HOME": "/tmp/fake-xdg"}, nil, "/tmp/fake-xdg/NexTerm"},
		{map[string]string{"NEXTERM_DATA_DIR": "/tmp/ignored"}, []string{"--data-dir", "/tmp/custom-agent-data"}, "/tmp/custom-agent-data"},
	} {
		h := newDeviceHarness(t, deviceOptions{})
		got := h.run(test.env, append(baseArgs(deviceVersion), test.args...)...)
		if got.code != 0 || !reflect.DeepEqual(h.calls(), expectedCalls(test.want, false)) {
			t.Fatalf("%v %v: %+v %v", test.env, test.args, got, h.calls())
		}
	}
}

func TestDeviceAgentFailures(t *testing.T) {
	h := newDeviceHarness(t, deviceOptions{})
	got := h.run(map[string]string{"FAKE_ENROLL_FAIL": "1"}, baseArgs(deviceVersion)...)
	if calls := h.calls(); got.code != 1 || len(calls) != 1 || calls[0][0] != "enroll" {
		t.Fatalf("enroll failure: %+v %v", got, calls)
	}
	h = newDeviceHarness(t, deviceOptions{})
	got = h.run(map[string]string{"FAKE_INSTALL_FAIL": "1"}, baseArgs(deviceVersion)...)
	if calls := h.calls(); got.code != 1 || len(calls) != 2 || calls[0][0] != "enroll" || calls[1][0] != "install" {
		t.Fatalf("install failure: %+v %v", got, calls)
	}
}
