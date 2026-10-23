package production

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/update"
)

func updateIPCDebArchive(t *testing.T, version string) (string, []byte) {
	t.Helper()
	return updateIPCDebArchiveWithPath(t, version, "usr/bin/nexterm-desktop")
}

func updateIPCDebArchiveWithPath(t *testing.T, version, binaryPath string) (string, []byte) {
	t.Helper()
	content := "new-binary-" + version
	var dataTarGz bytes.Buffer
	dataCompressed := gzip.NewWriter(&dataTarGz)
	dataWriter := tar.NewWriter(dataCompressed)
	if err := dataWriter.WriteHeader(&tar.Header{Name: binaryPath, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := dataWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dataCompressed.Close(); err != nil {
		t.Fatal(err)
	}
	var controlTarGz bytes.Buffer
	controlCompressed := gzip.NewWriter(&controlTarGz)
	controlWriter := tar.NewWriter(controlCompressed)
	control := "Package: nexterm\nVersion: " + version + "\n"
	if err := controlWriter.WriteHeader(&tar.Header{Name: "control", Mode: 0o644, Size: int64(len(control)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := controlWriter.Write([]byte(control)); err != nil {
		t.Fatal(err)
	}
	if err := controlWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := controlCompressed.Close(); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	archive.WriteString("!<arch>\n")
	writeMember := func(name string, payload []byte) {
		fmt.Fprintf(&archive, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name+"/", 0, 0, 0, "100644", len(payload))
		archive.Write(payload)
		if len(payload)%2 == 1 {
			archive.WriteByte('\n')
		}
	}
	writeMember("debian-binary", []byte("2.0\n"))
	writeMember("control.tar.gz", controlTarGz.Bytes())
	writeMember("data.tar.gz", dataTarGz.Bytes())
	return fmt.Sprintf("nexterm-desktop_linux_%s_%s.deb", runtime.GOARCH, version), archive.Bytes()
}

func updateIPCFixture(t *testing.T, version string) (*httptest.Server, string, []byte) {
	t.Helper()
	archiveName, archive := updateIPCDebArchive(t, version)
	return updateIPCFixtureArchive(t, version, archiveName, archive)
}

func updateIPCFixtureArchive(t *testing.T, version, archiveName string, archive []byte) (*httptest.Server, string, []byte) {
	t.Helper()
	digest := sha256.Sum256(archive)
	assets := []map[string]any{
		{"name": archiveName, "size": len(archive)},
		{"name": "nexterm-desktop_windows_amd64_" + version + ".exe"},
		{"name": "nexterm-desktop_windows_arm64_" + version + ".exe"},
		{"name": "nexterm-desktop_macos_arm64_" + version + ".dmg"},
		{"name": "nexterm-desktop_macos_amd64_" + version + ".dmg"},
		{"name": "SHA256SUMS"},
	}
	var sums strings.Builder
	for _, asset := range assets {
		name := asset["name"].(string)
		if name == archiveName {
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(digest[:]), name)
			continue
		}
		fmt.Fprintf(&sums, "%s  %s\n", strings.Repeat("0", 64), name)
	}
	releases := []map[string]any{{
		"tag_name": "v" + version, "body": "notes", "draft": false, "prerelease": false, "assets": assets,
	}}
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/releases", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(releases)
	})
	mux.HandleFunc("/assets/", func(response http.ResponseWriter, request *http.Request) {
		switch strings.TrimPrefix(request.URL.Path, "/assets/") {
		case "SHA256SUMS":
			_, _ = response.Write([]byte(sums.String()))
		case archiveName:
			_, _ = response.Write(archive)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	for index := range assets {
		assets[index]["browser_download_url"] = server.URL + "/assets/" + assets[index]["name"].(string)
	}
	return server, archiveName, archive
}

func newUpdateTestProduction(t *testing.T, desktop bool, recorder ipc.Emitter) *Production {
	t.Helper()
	config := ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{},
			Events:  recorder,
		},
		DataDir: t.TempDir(), Desktop: desktop,
	}
	production, err := NewProduction(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(t.Context()) })
	return production
}

func TestUpdateServerAssemblyDegrades(t *testing.T) {
	server, _, _ := updateIPCFixture(t, "0.3.0")
	production := newUpdateTestProduction(t, false, nil)
	if production.Services.Update == nil {
		t.Fatal("server assembly must compose the update manager")
	}
	production.Services.Update.APIURL = server.URL + "/releases"
	production.Services.Update.CurrentVersion = "0.2.2"

	response := dispatchHostKeyTest(t, production, "app_update_check", `null`)
	var status update.Status
	requireStoreTestResponse(t, response, &status)
	if !status.Available || status.CanInstall {
		t.Fatalf("server check = %+v, want available with canInstall=false", status)
	}

	response = dispatchHostKeyTest(t, production, "app_update_install", `null`)
	var install update.Result
	requireStoreTestResponse(t, response, &install)
	if install.Installed || !strings.Contains(install.UnavailableReason, "服务端模式") {
		t.Fatalf("server install = %+v", install)
	}

	response = dispatchHostKeyTest(t, production, "app_restart", `null`)
	var restart update.Result
	requireStoreTestResponse(t, response, &restart)
	if restart.Restarted || !strings.Contains(restart.UnavailableReason, "服务端模式") {
		t.Fatalf("server restart = %+v", restart)
	}
}

func TestUpdateInstallEmitsProgressEvents(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("install progress test drives the linux tarball plan")
	}
	server, _, archive := updateIPCFixture(t, "0.3.0")
	recorder := &ipcEventRecorder{}
	production := newUpdateTestProduction(t, true, recorder)
	manager := production.Services.Update
	manager.APIURL = server.URL + "/releases"
	manager.CurrentVersion = "0.2.2"
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager.ExePath = executable
	restarted := ""
	manager.RestartFunc = func(_ context.Context, target string) error {
		restarted = target
		return nil
	}

	response := dispatchHostKeyTest(t, production, "app_update_install", `null`)
	var result update.Result
	requireStoreTestResponse(t, response, &result)
	if !result.Installed || !result.Restarted || result.Version != "0.3.0" {
		t.Fatalf("install result = %+v", result)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-binary-0.3.0" {
		t.Fatalf("executable content = %q", content)
	}
	if restarted != executable {
		t.Fatalf("restart target = %q", restarted)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var progress []update.Progress
	for _, event := range recorder.events {
		if event.Event != ipc.TopicUpdateProgress {
			continue
		}
		payload, ok := event.Payload.(update.Progress)
		if !ok {
			t.Fatalf("progress payload type = %T", event.Payload)
		}
		progress = append(progress, payload)
	}
	if len(progress) < 3 {
		t.Fatalf("update progress events = %+v", progress)
	}
	if progress[0].Phase != update.PhaseDownload || progress[0].Total != int64(len(archive)) {
		t.Fatalf("first progress = %+v", progress[0])
	}
	last := progress[len(progress)-1]
	if last.Phase != update.PhaseInstall || !last.Done || last.Error != "" {
		t.Fatalf("last progress = %+v", last)
	}
}

type fakeUpdateCommandManager struct {
	installErr error
}

func (f *fakeUpdateCommandManager) Check(context.Context) update.Status {
	return update.Status{}
}

func (f *fakeUpdateCommandManager) Install(context.Context, string) (update.Result, error) {
	return update.Result{}, f.installErr
}

func (f *fakeUpdateCommandManager) Restart(context.Context) update.Result {
	return update.Result{}
}

func TestUpdateInstallIPCPreservesKnownUserErrors(t *testing.T) {
	tests := []struct {
		name    string
		kind    update.UserErrorKind
		message string
		code    ipc.Code
	}{
		{name: "concurrent install", kind: update.UserErrorBadParam, message: "正在安装更新，请稍候", code: ipc.CodeBadParam},
		{name: "version mismatch", kind: update.UserErrorBadParam, message: "待安装版本 0.2.2 与最新版本 0.3.0 不一致", code: ipc.CodeBadParam},
		{name: "version unavailable", kind: update.UserErrorNotFound, message: "版本 0.3.0 不存在或不可用", code: ipc.CodeNotFound},
		{name: "check failure", kind: update.UserErrorIO, message: "检查更新失败: GitHub Releases API 返回 503 Service Unavailable", code: ipc.CodeIO},
		{name: "checksum fetch failure", kind: update.UserErrorIO, message: "获取校验和失败: 下载 SHA256SUMS 返回 503 Service Unavailable", code: ipc.CodeIO},
		{name: "download failure", kind: update.UserErrorIO, message: "下载 NexTerm.deb 失败: connection reset", code: ipc.CodeIO},
		{name: "download status", kind: update.UserErrorIO, message: "下载 NexTerm.deb 返回 404 Not Found", code: ipc.CodeIO},
		{name: "download write failure", kind: update.UserErrorIO, message: "写入 NexTerm.deb 失败: disk full", code: ipc.CodeIO},
		{name: "digest failure", kind: update.UserErrorIO, message: "计算 NexTerm.deb 摘要失败: read failed", code: ipc.CodeIO},
		{name: "checksum missing", kind: update.UserErrorIO, message: "SHA256SUMS 缺少 NexTerm.deb 的校验和", code: ipc.CodeIO},
		{name: "checksum mismatch", kind: update.UserErrorIO, message: "NexTerm.deb 的 SHA256 校验失败：期望 expected，实际 actual", code: ipc.CodeIO},
		{name: "linux read failure", kind: update.UserErrorIO, message: "读取 NexTerm.deb 失败: unexpected EOF", code: ipc.CodeIO},
		{name: "linux replace failure", kind: update.UserErrorIO, message: "替换当前可执行文件失败: permission denied", code: ipc.CodeIO},
		{name: "darwin attach failure", kind: update.UserErrorIO, message: "hdiutil attach 失败: exit status 1 (invalid image)", code: ipc.CodeIO},
		{name: "darwin app missing", kind: update.UserErrorIO, message: "dmg 中没有 .app: NexTerm.dmg", code: ipc.CodeIO},
		{name: "darwin cleanup failure", kind: update.UserErrorIO, message: "清理旧应用失败: permission denied", code: ipc.CodeIO},
		{name: "darwin copy failure", kind: update.UserErrorIO, message: "复制 NexTerm.app 到 /Applications 失败: exit status 1 (copy failed)", code: ipc.CodeIO},
		{name: "windows executable missing", kind: update.UserErrorIO, message: "安装完成后未找到新可执行文件 C:\\NexTerm\\nexterm.exe", code: ipc.CodeIO},
		{name: "windows backup failure", kind: update.UserErrorIO, message: "备份当前可执行文件失败: permission denied", code: ipc.CodeIO},
		{name: "windows installer failure", kind: update.UserErrorIO, message: "NSIS 静默安装失败: exit status 1 (installer failed)", code: ipc.CodeIO},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			managerErr := &update.UserError{Kind: test.kind, Err: fmt.Errorf("%s", test.message)}
			dispatcher := ipc.NewDispatcher()
			if err := registerUpdateCommands(dispatcher, &fakeUpdateCommandManager{installErr: managerErr}); err != nil {
				t.Fatal(err)
			}
			response := dispatchStoreTest(dispatcher, "app_update_install", `null`)
			if response.OK || response.Error == nil {
				t.Fatalf("install response = %+v", response)
			}
			if response.Error.Code != test.code || response.Error.Message != test.message {
				t.Fatalf("install error = %+v, want code %q and message %q", response.Error, test.code, test.message)
			}
			if response.Error.Unwrap() != managerErr {
				t.Fatalf("install cause = %v, want original manager error", response.Error.Unwrap())
			}
		})
	}
}

func TestUpdateInstallIPCPreservesLinuxDebErrors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux deb install errors")
	}
	tests := []struct {
		name    string
		archive func(*testing.T) (string, []byte)
		message func(string) string
	}{
		{
			name: "invalid deb",
			archive: func(t *testing.T) (string, []byte) {
				return fmt.Sprintf("nexterm-desktop_linux_%s_0.3.0.deb", runtime.GOARCH), []byte("not a deb")
			},
			message: func(string) string { return "不是合法的 deb 包" },
		},
		{
			name: "gzip failure",
			archive: func(t *testing.T) (string, []byte) {
				name, archive := updateIPCDebArchive(t, "0.3.0")
				index := bytes.Index(archive, []byte("data.tar.gz/"))
				if index < 0 {
					t.Fatal("data.tar.gz member not found")
				}
				archive[index+60] = 0
				return name, archive
			},
			message: func(name string) string { return "解压 " + name + " 失败: gzip: invalid header" },
		},
		{
			name: "executable missing",
			archive: func(t *testing.T) (string, []byte) {
				return updateIPCDebArchiveWithPath(t, "0.3.0", "usr/bin/other")
			},
			message: func(name string) string { return name + " 中没有 nexterm-desktop" },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archiveName, archive := test.archive(t)
			server, _, _ := updateIPCFixtureArchive(t, "0.3.0", archiveName, archive)
			manager := &update.Manager{
				CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: runtime.GOARCH, CanInstall: true,
				ExePath: filepath.Join(t.TempDir(), "nexterm-desktop"), APIURL: server.URL + "/releases",
			}
			dispatcher := ipc.NewDispatcher()
			if err := registerUpdateCommands(dispatcher, manager); err != nil {
				t.Fatal(err)
			}
			response := dispatchStoreTest(dispatcher, "app_update_install", `null`)
			wantMessage := test.message(archiveName)
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeIO || response.Error.Message != wantMessage {
				t.Fatalf("install response = %+v, want io error %q", response, wantMessage)
			}
			cause, ok := response.Error.Unwrap().(*update.UserError)
			if !ok || cause.Kind != update.UserErrorIO || cause.Error() != wantMessage {
				t.Fatalf("install cause = %+v, want typed user error %q", response.Error.Unwrap(), wantMessage)
			}
		})
	}
}

func TestUpdateInstallIPCPreservesPlatformInstallerFailure(t *testing.T) {
	var archiveName, wantPrefix string
	switch runtime.GOOS {
	case "darwin":
		archiveName = "nexterm-desktop_macos_" + runtime.GOARCH + "_0.3.0.dmg"
		wantPrefix = "hdiutil attach 失败: "
	case "windows":
		archiveName = "nexterm-desktop_windows_" + runtime.GOARCH + "_0.3.0.exe"
		wantPrefix = "NSIS 静默安装失败: "
	default:
		t.Skip("macOS and Windows installer errors")
	}
	server, _, _ := updateIPCFixtureArchive(t, "0.3.0", archiveName, []byte("invalid installer"))
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &update.Manager{
		CurrentVersion: "0.2.2", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, CanInstall: true,
		ExePath: executable, APIURL: server.URL + "/releases",
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerUpdateCommands(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	response := dispatchStoreTest(dispatcher, "app_update_install", `null`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeIO || !strings.HasPrefix(response.Error.Message, wantPrefix) {
		t.Fatalf("install response = %+v, want io error with prefix %q", response, wantPrefix)
	}
	cause, ok := response.Error.Unwrap().(*update.UserError)
	if !ok || cause.Kind != update.UserErrorIO || cause.Error() != response.Error.Message {
		t.Fatalf("install cause = %+v, want the preserved installer error", response.Error.Unwrap())
	}
}

func TestUpdateInstallIPCHidesUnknownErrors(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	managerErr := fmt.Errorf("raw internal detail")
	dispatcher := ipc.NewDispatcher()
	if err := registerUpdateCommands(dispatcher, &fakeUpdateCommandManager{installErr: managerErr}); err != nil {
		t.Fatal(err)
	}
	response := dispatchStoreTest(dispatcher, "app_update_install", `null`)
	if response.OK || response.Error == nil {
		t.Fatalf("install response = %+v", response)
	}
	if response.Error.Code != ipc.CodeInternal || response.Error.Message != "内部错误，请稍后重试" {
		t.Fatalf("install error = %+v", response.Error)
	}
	if strings.Contains(response.Error.Message, managerErr.Error()) {
		t.Fatalf("install message leaks internal detail: %q", response.Error.Message)
	}
	if response.Error.Unwrap() != managerErr {
		t.Fatalf("install cause = %v, want original manager error", response.Error.Unwrap())
	}
	if output := logs.String(); !strings.Contains(output, "internal IPC error") || !strings.Contains(output, managerErr.Error()) {
		t.Fatalf("log output = %q, want original internal error", output)
	}
}
