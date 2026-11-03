package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/releasesig"
	"github.com/Hello-CTF/NexTerm/internal/releasesig/releasesigtest"
)

func buildTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildReleaseArchive(t *testing.T, goos string, content []byte) []byte {
	t.Helper()
	if goos == "windows" {
		return buildZip(t, agentBinaryName(goos), content)
	}
	return buildTarGz(t, agentBinaryName(goos), content)
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

var testReleaseKey = releasesigtest.NewKey(7)

// signedChecksums returns the SHA256SUMS and SHA256SUMS.minisig entries of a release, signed by testReleaseKey.
func signedChecksums(version, sums string) map[string][]byte {
	return map[string][]byte{
		"/v" + version + "/" + checksumsFileName:              []byte(sums),
		"/v" + version + "/" + checksumsFileName + ".minisig": []byte(testReleaseKey.Sign([]byte(sums), releasesig.ChecksumsTrustedComment(version))),
	}
}

func mergeFiles(parts ...map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for _, part := range parts {
		for key, value := range part {
			out[key] = value
		}
	}
	return out
}

type releaseServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []string
}

func newReleaseServer(t *testing.T, files map[string][]byte) *releaseServer {
	t.Helper()
	server := &releaseServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.requests = append(server.requests, r.URL.Path)
		server.mu.Unlock()
		content, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *releaseServer) requestCount(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, path := range s.requests {
		if strings.HasPrefix(path, prefix) {
			count++
		}
	}
	return count
}

type stagedReplace struct {
	newPath    string
	targetPath string
}

func captureReplace(captured *[]stagedReplace) func(string, string) error {
	return func(newPath, targetPath string) error {
		*captured = append(*captured, stagedReplace{newPath: newPath, targetPath: targetPath})
		return nil
	}
}

func instantSleep(context.Context, time.Duration) bool { return true }

func TestValidateReleaseVersion(t *testing.T) {
	valid := []string{"1.2.3", "0.2.2-rc.7", "10.0.0-beta.1"}
	for _, value := range valid {
		if err := validateReleaseVersion(value); err != nil {
			t.Fatalf("validateReleaseVersion(%q) = %v", value, err)
		}
	}
	invalid := []string{"", "v1.2.3/x", "../etc", "1.2.3;rm", "a b", strings.Repeat("1", 65)}
	for _, value := range invalid {
		if err := validateReleaseVersion(value); err == nil {
			t.Fatalf("validateReleaseVersion(%q) must fail", value)
		}
	}
}

func TestReleaseAssetName(t *testing.T) {
	if got := releaseAssetName("1.2.3", "linux", "amd64"); got != "nexterm-agent_linux_amd64_1.2.3.tar.gz" {
		t.Fatalf("linux asset = %q", got)
	}
	if got := releaseAssetName("1.2.3", "darwin", "arm64"); got != "nexterm-agent_macos_arm64_1.2.3.tar.gz" {
		t.Fatalf("darwin asset = %q", got)
	}
	if got := releaseAssetName("1.2.3", "windows", "amd64"); got != "nexterm-agent_windows_amd64_1.2.3.zip" {
		t.Fatalf("windows asset = %q", got)
	}
	if agentBinaryName("windows") != "nexterm-agent.exe" || agentBinaryName("linux") != "nexterm-agent" {
		t.Fatal("agent binary names")
	}
}

func TestParseSHA256SUMS(t *testing.T) {
	digest := strings.Repeat("a", 64)
	content := "# comment\n\n" + digest + "  nexterm-agent_linux_amd64_1.2.3.tar.gz\n" +
		strings.Repeat("B", 64) + " *nexterm-agent_windows_amd64_1.2.3.zip\n" +
		"short\n"
	sums := parseSHA256SUMS([]byte(content))
	if sums["nexterm-agent_linux_amd64_1.2.3.tar.gz"] != digest {
		t.Fatalf("tar entry = %q", sums["nexterm-agent_linux_amd64_1.2.3.tar.gz"])
	}
	if sums["nexterm-agent_windows_amd64_1.2.3.zip"] != strings.Repeat("b", 64) {
		t.Fatalf("zip entry (binary marker, case folding) = %q", sums["nexterm-agent_windows_amd64_1.2.3.zip"])
	}
}

func TestUpgradeStagesVerifiedBinary(t *testing.T) {
	version := "1.2.3"
	asset := releaseAssetName(version, runtime.GOOS, runtime.GOARCH)
	binary := []byte("#!/bin/sh\necho new-agent\n")
	archive := buildReleaseArchive(t, runtime.GOOS, binary)
	sums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)
	server := newReleaseServer(t, mergeFiles(map[string][]byte{"/v" + version + "/" + asset: archive}, signedChecksums(version, sums)))

	targetDir := t.TempDir()
	target := filepath.Join(targetDir, agentBinaryName(runtime.GOOS))
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var replaced []stagedReplace
	upgrader := &Upgrader{
		BaseURL: server.URL, Executable: target, PublicKey: testReleaseKey.PublicKeyFile(),
		MaxAttempts: 3, Backoff: time.Millisecond,
		sleep: instantSleep, scheduleReplace: captureReplace(&replaced),
	}
	if err := upgrader.Upgrade(context.Background(), version); err != nil {
		t.Fatalf("Upgrade = %v", err)
	}
	if len(replaced) != 1 || replaced[0].targetPath != target {
		t.Fatalf("scheduleReplace calls = %+v", replaced)
	}
	staged := replaced[0].newPath
	content, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, binary) {
		t.Fatalf("staged content = %q", content)
	}
	info, err := os.Stat(staged)
	if err != nil {
		t.Fatal(err)
	}
	wantMode := os.FileMode(0o755)
	if runtime.GOOS == "windows" {
		wantMode = 0o666
	}
	if got := info.Mode().Perm(); got != wantMode {
		t.Fatalf("staged mode = %v, want %v", got, wantMode)
	}
	oldContent, err := os.ReadFile(target)
	if err != nil || string(oldContent) != "old-binary" {
		t.Fatalf("old binary must stay untouched until the deferred replace: %q, %v", oldContent, err)
	}
	if server.requestCount("/v"+version+"/"+asset) != 1 || server.requestCount("/v"+version+"/"+checksumsFileName) != 2 {
		t.Fatalf("requests = %v", server.requests)
	}
}

func TestUpgradeRejectsChecksumMismatch(t *testing.T) {
	version := "1.2.3"
	asset := releaseAssetName(version, runtime.GOOS, runtime.GOARCH)
	archive := buildTarGz(t, agentBinaryName(runtime.GOOS), []byte("agent"))
	sums := fmt.Sprintf("%s  %s\n", strings.Repeat("0", 64), asset)
	server := newReleaseServer(t, mergeFiles(map[string][]byte{"/v" + version + "/" + asset: archive}, signedChecksums(version, sums)))
	target := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var replaced []stagedReplace
	upgrader := &Upgrader{
		BaseURL: server.URL, Executable: target, PublicKey: testReleaseKey.PublicKeyFile(),
		MaxAttempts: 2, Backoff: time.Millisecond,
		sleep: instantSleep, scheduleReplace: captureReplace(&replaced),
	}
	err := upgrader.Upgrade(context.Background(), version)
	if err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("Upgrade = %v, want checksum failure", err)
	}
	if len(replaced) != 0 {
		t.Fatal("scheduleReplace must not run on checksum failure")
	}
	if server.requestCount("/v"+version+"/"+asset) != 2 {
		t.Fatalf("asset fetches = %d, want 2 (one per attempt)", server.requestCount("/v"+version+"/"+asset))
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("target dir must keep only the old binary, got %d entries", len(entries))
	}
}

func TestUpgradeMissingChecksumEntry(t *testing.T) {
	version := "1.2.3"
	asset := releaseAssetName(version, runtime.GOOS, runtime.GOARCH)
	archive := buildTarGz(t, agentBinaryName(runtime.GOOS), []byte("agent"))
	server := newReleaseServer(t, mergeFiles(map[string][]byte{"/v" + version + "/" + asset: archive}, signedChecksums(version, strings.Repeat("a", 64)+"  other-asset.tar.gz\n")))
	target := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	var replaced []stagedReplace
	upgrader := &Upgrader{
		BaseURL: server.URL, Executable: target, PublicKey: testReleaseKey.PublicKeyFile(),
		MaxAttempts: 1, scheduleReplace: captureReplace(&replaced),
	}
	err := upgrader.Upgrade(context.Background(), version)
	if err == nil || !strings.Contains(err.Error(), checksumsFileName) {
		t.Fatalf("Upgrade = %v, want missing checksum entry", err)
	}
	if len(replaced) != 0 {
		t.Fatal("scheduleReplace must not run without a checksum entry")
	}
}

func TestUpgradeRefusesWithoutPublicKey(t *testing.T) {
	previous := releasesig.EmbeddedPublicKey
	releasesig.EmbeddedPublicKey = ""
	t.Cleanup(func() { releasesig.EmbeddedPublicKey = previous })
	server := newReleaseServer(t, map[string][]byte{})
	var replaced []stagedReplace
	upgrader := &Upgrader{
		BaseURL: server.URL, Executable: filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS)),
		MaxAttempts: 3, Backoff: time.Millisecond, sleep: instantSleep, scheduleReplace: captureReplace(&replaced),
	}
	err := upgrader.Upgrade(context.Background(), "1.2.3")
	if err == nil || !errors.Is(err, errUpgradeRefused) || !strings.Contains(err.Error(), "签名公钥") {
		t.Fatalf("Upgrade = %v, want a refusal that names the missing signing key", err)
	}
	if got := server.requestCount("/"); got != 0 {
		t.Fatalf("a build without a signing key must not touch the network, got %d requests", got)
	}
	if len(replaced) != 0 {
		t.Fatal("scheduleReplace must not run")
	}
}

func TestUpgradeUsesEmbeddedPublicKey(t *testing.T) {
	previous := releasesig.EmbeddedPublicKey
	releasesig.EmbeddedPublicKey = testReleaseKey.EncodedPublic
	t.Cleanup(func() { releasesig.EmbeddedPublicKey = previous })
	version := "1.2.3"
	asset := releaseAssetName(version, runtime.GOOS, runtime.GOARCH)
	archive := buildReleaseArchive(t, runtime.GOOS, []byte("agent"))
	server := newReleaseServer(t, mergeFiles(map[string][]byte{"/v" + version + "/" + asset: archive},
		signedChecksums(version, fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset))))
	var replaced []stagedReplace
	upgrader := &Upgrader{
		BaseURL: server.URL, Executable: filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS)),
		MaxAttempts: 1, scheduleReplace: captureReplace(&replaced),
	}
	if err := upgrader.Upgrade(context.Background(), version); err != nil || len(replaced) != 1 {
		t.Fatalf("Upgrade = %v, replaced %d", err, len(replaced))
	}
}

func TestUpgradeRefusesUnverifiedChecksums(t *testing.T) {
	version := "1.2.3"
	asset := releaseAssetName(version, runtime.GOOS, runtime.GOARCH)
	archive := buildReleaseArchive(t, runtime.GOOS, []byte("agent"))
	sums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)
	sumsPath := "/v" + version + "/" + checksumsFileName
	sigPath := sumsPath + ".minisig"
	otherKey := releasesigtest.NewKey(8)
	tests := []struct {
		name  string
		files func() map[string][]byte
		key   string
	}{
		{name: "missing signature", files: func() map[string][]byte {
			files := signedChecksums(version, sums)
			delete(files, sigPath)
			return files
		}},
		{name: "signed by another key", files: func() map[string][]byte {
			return map[string][]byte{sumsPath: []byte(sums), sigPath: []byte(otherKey.Sign([]byte(sums), releasesig.ChecksumsTrustedComment(version)))}
		}},
		{name: "trusted comment of another version", files: func() map[string][]byte {
			return map[string][]byte{sumsPath: []byte(sums), sigPath: []byte(testReleaseKey.Sign([]byte(sums), releasesig.ChecksumsTrustedComment("1.2.2")))}
		}},
		{name: "checksums changed after signing", files: func() map[string][]byte {
			files := signedChecksums(version, sums)
			files[sumsPath] = []byte(fmt.Sprintf("%s  %s\n", strings.Repeat("0", 64), asset))
			return files
		}},
		{name: "invalid trusted key", key: "not-a-key", files: func() map[string][]byte { return signedChecksums(version, sums) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newReleaseServer(t, mergeFiles(map[string][]byte{"/v" + version + "/" + asset: archive}, test.files()))
			target := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
			if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			publicKey := test.key
			if publicKey == "" {
				publicKey = testReleaseKey.PublicKeyFile()
			}
			var replaced []stagedReplace
			var sleeps int
			upgrader := &Upgrader{
				BaseURL: server.URL, Executable: target, PublicKey: publicKey,
				MaxAttempts: 3, Backoff: time.Millisecond,
				sleep:           func(context.Context, time.Duration) bool { sleeps++; return true },
				scheduleReplace: captureReplace(&replaced),
			}
			err := upgrader.Upgrade(context.Background(), version)
			if !errors.Is(err, errUpgradeRefused) {
				t.Fatalf("Upgrade = %v, want a permanent refusal", err)
			}
			if len(replaced) != 0 {
				t.Fatal("scheduleReplace must not run")
			}
			if got := server.requestCount("/v" + version + "/" + asset); got != 0 {
				t.Fatalf("the archive must not be downloaded before the signature verifies, got %d requests", got)
			}
			if got := server.requestCount(sumsPath); got > 2 {
				t.Fatalf("a refusal must not be retried, got %d checksum requests", got)
			}
			if sleeps != 0 {
				t.Fatalf("a refusal must not back off, slept %d times", sleeps)
			}
			content, readErr := os.ReadFile(target)
			if readErr != nil || string(content) != "old-binary" {
				t.Fatalf("old binary must stay untouched: %q, %v", content, readErr)
			}
		})
	}
}

func TestUpgradeRetryCapAndBackoff(t *testing.T) {
	version := "9.9.9"
	server := newReleaseServer(t, map[string][]byte{})
	target := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	var replaced []stagedReplace
	var sleeps []time.Duration
	upgrader := &Upgrader{
		BaseURL: server.URL, Executable: target, PublicKey: testReleaseKey.PublicKeyFile(),
		MaxAttempts: 3, Backoff: time.Millisecond,
		sleep: func(_ context.Context, d time.Duration) bool {
			sleeps = append(sleeps, d)
			return true
		},
		scheduleReplace: captureReplace(&replaced),
	}
	err := upgrader.Upgrade(context.Background(), version)
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("Upgrade = %v, want retry cap error", err)
	}
	if got := server.requestCount("/v" + version + "/"); got != 3 {
		t.Fatalf("asset fetches = %d, want 3", got)
	}
	if len(sleeps) != 2 || sleeps[0] != time.Millisecond || sleeps[1] != 2*time.Millisecond {
		t.Fatalf("backoff sleeps = %v, want exponential [1ms 2ms]", sleeps)
	}
	if len(replaced) != 0 {
		t.Fatal("scheduleReplace must not run when downloads fail")
	}
}

func TestUpgradeInvalidVersionSkipsNetwork(t *testing.T) {
	server := newReleaseServer(t, map[string][]byte{})
	upgrader := &Upgrader{BaseURL: server.URL, Executable: filepath.Join(t.TempDir(), "nexterm-agent"), PublicKey: testReleaseKey.PublicKeyFile()}
	if err := upgrader.Upgrade(context.Background(), "../1.0"); err == nil {
		t.Fatal("invalid version must fail")
	}
	if got := server.requestCount("/"); got != 0 {
		t.Fatalf("invalid version must not hit the network, got %d requests", got)
	}
}

func TestExtractAgentBinaryFromZip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "nexterm-agent_windows_amd64_1.2.3.zip")
	if err := os.WriteFile(archivePath, buildZip(t, "nexterm-agent.exe", []byte("mz-agent")), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "staged.exe")
	if err := extractAgentBinary(archivePath, dest, "nexterm-agent.exe"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(dest)
	if err != nil || string(content) != "mz-agent" {
		t.Fatalf("extracted = %q, %v", content, err)
	}
	if err := extractAgentBinary(archivePath, filepath.Join(dir, "other.exe"), "nexterm-agent"); err == nil {
		t.Fatal("missing inner binary must fail")
	}
}

func TestRenderReplaceScriptUnix(t *testing.T) {
	script := renderReplaceScriptUnix("/opt/bin/.nexterm-agent.nexterm-new", "/opt/bin/nexterm-agent", 3, unixRestartCommand("linux"))
	for _, fragment := range []string{
		"#!/bin/sh\nsleep 3\n",
		"mv -f '/opt/bin/nexterm-agent' '/opt/bin/nexterm-agent.nexterm-old'",
		"if mv -f '/opt/bin/.nexterm-agent.nexterm-new' '/opt/bin/nexterm-agent'; then",
		"chmod 0755 '/opt/bin/nexterm-agent'",
		"  systemctl --user restart nexterm-agent.service 2>/dev/null || true\n",
		"mv -f '/opt/bin/nexterm-agent.nexterm-old' '/opt/bin/nexterm-agent'",
		"rm -f \"$0\"",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("script missing %q:\n%s", fragment, script)
		}
	}
	if strings.Contains(script, "launchctl") {
		t.Fatal("linux script must not kickstart launchd")
	}
	darwin := renderReplaceScriptUnix("/a/new", "/a/old", 3, unixRestartCommand("darwin"))
	if !strings.Contains(darwin, `launchctl kickstart "gui/$(id -u)/com.nexterm.agent"`) {
		t.Fatalf("darwin script missing kickstart:\n%s", darwin)
	}
	if strings.Contains(darwin, "systemctl") {
		t.Fatal("darwin script must not restart systemd")
	}
	quoted := renderReplaceScriptUnix("/dir with spaces/new", "/dir with spaces/old", 3, "true")
	if !strings.Contains(quoted, "'/dir with spaces/old'") {
		t.Fatalf("paths with spaces must be quoted:\n%s", quoted)
	}
}

func TestRenderReplaceScriptWindows(t *testing.T) {
	script := renderReplaceScriptWindows(`C:\bin\.nexterm-agent.exe.nexterm-new`, `C:\bin\nexterm-agent.exe`, 3, windowsRestartCommand())
	for _, fragment := range []string{
		"Start-Sleep -Seconds 3",
		"Remove-Item -LiteralPath $PSCommandPath",
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("script missing %q:\n%s", fragment, script)
		}
	}
	if !strings.Contains(script, "$nxTarget = 'C:\\bin\\nexterm-agent.exe'") {
		t.Fatalf("target must be single-quoted:\n%s", script)
	}
	// The swap-in runs in the multi-line try block; its catch block is the rollback.
	_, afterTry, ok := strings.Cut(script, "try {\n")
	if !ok {
		t.Fatalf("script has no swap-in try block:\n%s", script)
	}
	success, rollback, ok := strings.Cut(afterTry, "\n} catch {\n")
	if !ok {
		t.Fatalf("script has no rollback catch block:\n%s", script)
	}
	rollback, _, _ = strings.Cut(rollback, "\n}\n")
	restart := windowsRestartCommand()
	swap := strings.Index(success, "Move-Item -LiteralPath $nxNew -Destination $nxTarget -Force")
	restartAt := strings.Index(success, restart)
	if swap < 0 || restartAt < 0 || restartAt < swap {
		t.Fatalf("restart must follow the successful swap-in:\n%s", script)
	}
	if !strings.Contains(restart, "schtasks.exe") || !strings.Contains(restart, "/Run") || !strings.Contains(restart, "'NexTermAgent'") {
		t.Fatalf("restart command must run the quoted scheduled task: %s", restart)
	}
	if !strings.Contains(rollback, "Move-Item -LiteralPath $nxBak -Destination $nxTarget -Force") {
		t.Fatalf("rollback block must restore the backup:\n%s", script)
	}
	if strings.Contains(rollback, "schtasks") || strings.Contains(rollback, restart) {
		t.Fatalf("rollback must not restart the task:\n%s", script)
	}
	if got := psQuote("o'brien"); got != "'o''brien'" {
		t.Fatalf("psQuote escaping = %q", got)
	}
	if got := shQuote("o'brien"); got != `'o'\''brien'` {
		t.Fatalf("shQuote escaping = %q", got)
	}
}

func TestReplaceScriptUnixExecutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell script")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "nexterm-agent")

	writeScript := func(newPath string) string {
		t.Helper()
		scriptPath := filepath.Join(dir, "replace.sh")
		if err := os.WriteFile(scriptPath, []byte(renderReplaceScriptUnix(newPath, target, 0, "true")), 0o755); err != nil {
			t.Fatal(err)
		}
		return scriptPath
	}

	t.Run("successful swap and cleanup", func(t *testing.T) {
		if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		newPath := filepath.Join(dir, ".nexterm-agent.nexterm-new")
		if err := os.WriteFile(newPath, []byte("new-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		scriptPath := writeScript(newPath)
		if output, err := exec.Command("sh", scriptPath).CombinedOutput(); err != nil {
			t.Fatalf("script failed: %v\n%s", err, output)
		}
		content, err := os.ReadFile(target)
		if err != nil || string(content) != "new-binary" {
			t.Fatalf("target = %q, %v", content, err)
		}
		if _, err := os.Stat(target + ".nexterm-old"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("backup must be removed after a successful swap")
		}
		if _, err := os.Stat(scriptPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("script must remove itself")
		}
	})

	t.Run("failed swap rolls back old binary", func(t *testing.T) {
		if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(dir, ".nexterm-agent.nexterm-new")
		_ = os.Remove(missing)
		scriptPath := writeScript(missing)
		if output, err := exec.Command("sh", scriptPath).CombinedOutput(); err != nil {
			t.Fatalf("script failed: %v\n%s", err, output)
		}
		content, err := os.ReadFile(target)
		if err != nil || string(content) != "old-binary" {
			t.Fatalf("rollback must restore the old binary, target = %q, %v", content, err)
		}
	})
}
