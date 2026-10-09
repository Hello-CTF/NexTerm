package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type releaseFixture struct {
	releases    []Release
	checksums   string
	archive     []byte
	archiveName string
	status      int
}

func (fixture *releaseFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/releases", func(response http.ResponseWriter, _ *http.Request) {
		if fixture.status != 0 && fixture.status != http.StatusOK {
			response.WriteHeader(fixture.status)
			return
		}
		_ = json.NewEncoder(response).Encode(fixture.releases)
	})
	mux.HandleFunc("/assets/", func(response http.ResponseWriter, request *http.Request) {
		name := strings.TrimPrefix(request.URL.Path, "/assets/")
		if name == checksumsAssetName {
			_, _ = response.Write([]byte(fixture.checksums))
			return
		}
		if name == fixture.archiveName {
			_, _ = response.Write(fixture.archive)
			return
		}
		response.WriteHeader(http.StatusNotFound)
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	for index := range fixture.releases {
		for assetIndex := range fixture.releases[index].Assets {
			asset := &fixture.releases[index].Assets[assetIndex]
			asset.BrowserDownloadURL = server.URL + "/assets/" + asset.Name
		}
	}
	return server
}

func tarGz(t *testing.T, name, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func debArchive(t *testing.T, version, content string) []byte {
	t.Helper()
	dataTarGz := tarGz(t, "usr/bin/nexterm-desktop", content)
	controlTarGz := tarGz(t, "control", "Package: nexterm\nVersion: "+version+"\n")
	var buffer bytes.Buffer
	buffer.WriteString("!<arch>\n")
	writeMember := func(name string, payload []byte) {
		fmt.Fprintf(&buffer, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name+"/", 0, 0, 0, "100644", len(payload))
		buffer.Write(payload)
		if len(payload)%2 == 1 {
			buffer.WriteByte('\n')
		}
	}
	writeMember("debian-binary", []byte("2.0\n"))
	writeMember("control.tar.gz", controlTarGz)
	writeMember("data.tar.gz", dataTarGz)
	return buffer.Bytes()
}

func sumsFor(archiveName string, archive []byte) string {
	digest := sha256.Sum256(archive)
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName)
}

func linuxFixture(t *testing.T, version string, corruptChecksum bool) *releaseFixture {
	t.Helper()
	archiveName := "NexTerm-desktop_" + version + "_linux_amd64.deb"
	archive := debArchive(t, version, "new-binary-"+version)
	checksums := sumsFor(archiveName, archive)
	if corruptChecksum {
		checksums = strings.Repeat("0", 64) + "  " + archiveName + "\n"
	}
	return &releaseFixture{
		releases: []Release{{
			TagName: "v" + version,
			Body:    "release notes",
			Assets: []Asset{
				{Name: archiveName, Size: int64(len(archive))},
				{Name: checksumsAssetName},
			},
		}},
		checksums:   checksums,
		archive:     archive,
		archiveName: archiveName,
	}
}

func TestCheckFindsUpdate(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", false)
	server := fixture.server(t)
	manager := &Manager{CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, APIURL: server.URL + "/releases"}
	status := manager.Check(context.Background())
	if !status.Available || status.Version != "0.3.0" || status.AssetName != fixture.archiveName {
		t.Fatalf("status = %+v", status)
	}
	if !status.CanInstall || status.UnavailableReason != "" {
		t.Fatalf("status = %+v", status)
	}
	if status.CurrentVersion != "0.2.2" || status.Notes != "release notes" {
		t.Fatalf("status = %+v", status)
	}
}

func TestCheckUpToDateHasNoReason(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", false)
	server := fixture.server(t)
	manager := &Manager{CurrentVersion: "0.3.0", GOOS: "linux", GOARCH: "amd64", CanInstall: true, APIURL: server.URL + "/releases"}
	status := manager.Check(context.Background())
	if status.Available || status.UnavailableReason != "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestCheckDegradesToUnavailableReason(t *testing.T) {
	cases := []struct {
		name     string
		manager  func(server *httptest.Server) *Manager
		fixture  func(t *testing.T) *releaseFixture
		contains string
	}{
		{
			name: "api failure",
			manager: func(server *httptest.Server) *Manager {
				return &Manager{CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", APIURL: server.URL + "/releases"}
			},
			fixture: func(t *testing.T) *releaseFixture {
				fixture := linuxFixture(t, "0.3.0", false)
				fixture.status = http.StatusInternalServerError
				return fixture
			},
			contains: "检查更新失败",
		},
		{
			name: "missing asset",
			manager: func(server *httptest.Server) *Manager {
				return &Manager{CurrentVersion: "0.2.2", GOOS: "windows", GOARCH: "amd64", APIURL: server.URL + "/releases"}
			},
			fixture:  func(t *testing.T) *releaseFixture { return linuxFixture(t, "0.3.0", false) },
			contains: "没有适合当前平台的安装包",
		},
		{
			name: "missing checksum entry",
			manager: func(server *httptest.Server) *Manager {
				return &Manager{CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", APIURL: server.URL + "/releases"}
			},
			fixture: func(t *testing.T) *releaseFixture {
				fixture := linuxFixture(t, "0.3.0", false)
				fixture.checksums = strings.Repeat("0", 64) + "  other-file\n"
				return fixture
			},
			contains: "缺少",
		},
		{
			name: "unsupported platform",
			manager: func(server *httptest.Server) *Manager {
				return &Manager{CurrentVersion: "0.2.2", GOOS: "plan9", GOARCH: "amd64", APIURL: server.URL + "/releases"}
			},
			fixture:  func(t *testing.T) *releaseFixture { return linuxFixture(t, "0.3.0", false) },
			contains: "不支持应用内安装",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := test.fixture(t).server(t)
			status := test.manager(server).Check(context.Background())
			if status.Available || !strings.Contains(status.UnavailableReason, test.contains) {
				t.Fatalf("status = %+v, want reason containing %q", status, test.contains)
			}
		})
	}
}

func TestCheckServerModeReportsCanInstallFalse(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", false)
	server := fixture.server(t)
	manager := &Manager{CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: false, APIURL: server.URL + "/releases"}
	status := manager.Check(context.Background())
	if !status.Available || status.CanInstall {
		t.Fatalf("status = %+v", status)
	}
}

func TestInstallReplacesBinaryEmitsProgressAndRestarts(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", false)
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var progress []Progress
	restartTarget := ""
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
		ExePath: executable, APIURL: server.URL + "/releases",
		Emit: func(_ context.Context, event Progress) { progress = append(progress, event) },
		RestartFunc: func(_ context.Context, target string) error {
			restartTarget = target
			return nil
		},
	}
	result, err := manager.Install(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.Restarted || result.Version != "0.3.0" {
		t.Fatalf("result = %+v", result)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-binary-0.3.0" {
		t.Fatalf("executable content = %q", content)
	}
	if restartTarget != executable {
		t.Fatalf("restart target = %q, want %q", restartTarget, executable)
	}
	if len(progress) < 3 {
		t.Fatalf("progress events = %+v", progress)
	}
	if progress[0].Phase != PhaseDownload || progress[0].Total != int64(len(fixture.archive)) {
		t.Fatalf("first progress = %+v", progress[0])
	}
	last := progress[len(progress)-1]
	if last.Phase != PhaseInstall || !last.Done || last.Error != "" {
		t.Fatalf("last progress = %+v", last)
	}
}

func TestInstallChecksumMismatchKeepsOldBinary(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", true)
	server := fixture.server(t)
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var progress []Progress
	manager := &Manager{
		CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
		ExePath: executable, APIURL: server.URL + "/releases",
		Emit: func(_ context.Context, event Progress) { progress = append(progress, event) },
	}
	if _, err := manager.Install(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "SHA256 校验失败") {
		t.Fatalf("install error = %v", err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old-binary" {
		t.Fatalf("executable must not be replaced, content = %q", content)
	}
	last := progress[len(progress)-1]
	if !last.Done || last.Error == "" {
		t.Fatalf("last progress = %+v", last)
	}
}

func TestInstallRejectsVersionMismatch(t *testing.T) {
	fixture := linuxFixture(t, "0.3.0", false)
	server := fixture.server(t)
	manager := &Manager{CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true, APIURL: server.URL + "/releases"}
	if _, err := manager.Install(context.Background(), "9.9.9"); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("install error = %v", err)
	}
}

func TestInstallServerModeDegrades(t *testing.T) {
	manager := &Manager{CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: false, APIURL: "http://127.0.0.1:1/releases"}
	result, err := manager.Install(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Installed || !strings.Contains(result.UnavailableReason, "server 模式") {
		t.Fatalf("result = %+v", result)
	}
	restart := manager.Restart(context.Background())
	if restart.Restarted || !strings.Contains(restart.UnavailableReason, "server 模式") {
		t.Fatalf("restart = %+v", restart)
	}
}

func TestRestartUsesInjectedFunc(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "nexterm-desktop")
	target := ""
	manager := &Manager{ExePath: executable, RestartFunc: func(_ context.Context, path string) error {
		target = path
		return nil
	}}
	result := manager.Restart(context.Background())
	if !result.Restarted || target != executable {
		t.Fatalf("result = %+v, target = %q", result, target)
	}
}
