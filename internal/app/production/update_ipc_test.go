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

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/update"
)

func updateIPCDebArchive(t *testing.T, version string) (string, []byte) {
	t.Helper()
	content := "new-binary-" + version
	var dataTarGz bytes.Buffer
	dataCompressed := gzip.NewWriter(&dataTarGz)
	dataWriter := tar.NewWriter(dataCompressed)
	if err := dataWriter.WriteHeader(&tar.Header{Name: "usr/bin/nexterm-desktop", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
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
	return fmt.Sprintf("NexTerm-desktop_%s_linux_%s.deb", version, runtime.GOARCH), archive.Bytes()
}

func updateIPCFixture(t *testing.T, version string) (*httptest.Server, string, []byte) {
	t.Helper()
	archiveName, archive := updateIPCDebArchive(t, version)
	digest := sha256.Sum256(archive)
	assets := []map[string]any{
		{"name": archiveName, "size": len(archive)},
		{"name": "NexTerm_" + version + "_x64-setup.exe"},
		{"name": "NexTerm_" + version + "_arm64-setup.exe"},
		{"name": "NexTerm_" + version + "_aarch64.dmg"},
		{"name": "NexTerm_" + version + "_x86_64.dmg"},
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

func TestUpdateCommandsRegistered(t *testing.T) {
	production := newUpdateTestProduction(t, false, nil)
	for _, command := range []string{"app_update_check", "app_update_install", "app_restart"} {
		found := false
		for _, registered := range production.Dispatcher.Commands() {
			if registered == command {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("production is missing %s", command)
		}
	}
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
	if install.Installed || !strings.Contains(install.UnavailableReason, "server 模式") {
		t.Fatalf("server install = %+v", install)
	}

	response = dispatchHostKeyTest(t, production, "app_restart", `null`)
	var restart update.Result
	requireStoreTestResponse(t, response, &restart)
	if restart.Restarted || !strings.Contains(restart.UnavailableReason, "server 模式") {
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
