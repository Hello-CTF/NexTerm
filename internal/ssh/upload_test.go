package ssh

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildInspectionBinary(t *testing.T, goos, goarch, goarm string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.invalid/inspection\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "helper"
	if goos == "windows" {
		name += ".exe"
	}
	output := filepath.Join(directory, name)
	command := exec.Command("go", "build", "-trimpath", "-o", output, ".")
	command.Dir = directory
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch, "GOARM="+goarm, "GOFLAGS=-buildvcs=false")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s/%s GOARM=%s: %v: %s", goos, goarch, goarm, err, raw)
	}
	return output
}

func contentSHA256(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func TestInspectExecutableRequiresArmv7MetadataAndHashesContent(t *testing.T) {
	path := buildInspectionBinary(t, "linux", "arm", "7")
	source, err := InspectExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if source.GOOS != "linux" || source.GOARCH != "arm" || source.GOARM != "7" {
		t.Fatalf("armv7 metadata = %+v", source)
	}
	if source.Digest != contentSHA256(t, path) {
		t.Fatalf("digest = %q, want content SHA256", source.Digest)
	}
	if source.CGO {
		t.Fatal("CGO_ENABLED=0 binary marked as CGO")
	}
	if source.Version != "" || source.Commit != "" || source.ManifestPath != "" {
		t.Fatalf("unstamped build invented release evidence: %+v", source)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version": 1,
		"kind":           "go-binary",
		"version":        "v1.2.3",
		"platform":       map[string]any{"os": "linux", "arch": "armv7", "cgo": "0", "goarm": "7"},
		"artifact":       map[string]any{"size_bytes": info.Size(), "sha256": source.Digest},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(path+".manifest.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err = InspectExecutable(path)
	if err != nil {
		t.Fatalf("armv7 manifest with packaging architecture label: %v", err)
	}
	if source.GOARCH != "arm" || source.GOARM != "7" || source.Version != "v1.2.3" {
		t.Fatalf("armv7 manifest evidence = %+v", source)
	}
}

func TestInspectExecutableUsesVerifiedManifestEvidence(t *testing.T) {
	path := buildInspectionBinary(t, runtime.GOOS, runtime.GOARCH, "")
	digest := contentSHA256(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := path + ".manifest.json"
	manifest := map[string]any{
		"schema_version": 1,
		"kind":           "go-binary",
		"version":        "v1.2.3",
		"commit":         strings.Repeat("a", 40),
		"platform": map[string]any{
			"os": runtime.GOOS, "arch": runtime.GOARCH, "cgo": "0",
		},
		"artifact": map[string]any{"size_bytes": info.Size(), "sha256": digest},
		"sha256":   digest,
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := InspectExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if source.Version != "v1.2.3" || source.Commit != strings.Repeat("a", 40) || source.ManifestPath != manifestPath {
		t.Fatalf("manifest evidence = %+v", source)
	}

	manifest["artifact"] = map[string]any{"size_bytes": info.Size(), "sha256": strings.Repeat("0", 64)}
	raw, _ = json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = InspectExecutable(path)
	if !errors.Is(err, ErrDigestMismatch) || !errors.Is(err, ErrTrust) {
		t.Fatalf("manifest digest mismatch = %v, want digest and trust causes", err)
	}
}

func TestInspectExecutableRejectsManifestArchitectureMismatch(t *testing.T) {
	path := buildInspectionBinary(t, runtime.GOOS, runtime.GOARCH, "")
	manifest := fmt.Sprintf(`{"schema_version":1,"kind":"go-binary","platform":{"os":%q,"arch":"amd64","cgo":"0"},"artifact":{"size_bytes":0,"sha256":%q}}`, runtime.GOOS, contentSHA256(t, path))
	if runtime.GOARCH == "amd64" {
		manifest = strings.Replace(manifest, `"arch":"amd64"`, `"arch":"arm64"`, 1)
	}
	if err := os.WriteFile(path+".manifest.json", []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectExecutable(path); !errors.Is(err, ErrTrust) {
		t.Fatalf("architecture mismatch = %v, want trust failure", err)
	}
}

func buildVersionedInspectionBinary(t *testing.T, version string) string {
	t.Helper()
	const module = "example.invalid/inspection"
	directory := t.TempDir()
	proxyRoot := filepath.Join(directory, "proxy")
	versions := filepath.Join(proxyRoot, module, "@v")
	if err := os.MkdirAll(versions, 0o700); err != nil {
		t.Fatal(err)
	}
	goMod := []byte("module " + module + "\n\ngo 1.26\n")
	if err := os.WriteFile(filepath.Join(versions, version+".mod"), goMod, 0o600); err != nil {
		t.Fatal(err)
	}
	info := fmt.Sprintf(`{"Version":%q,"Time":"2024-01-01T00:00:00Z"}`, version)
	if err := os.WriteFile(filepath.Join(versions, version+".info"), []byte(info), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versions, "list"), []byte(version+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Create(filepath.Join(versions, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(archive)
	for _, entry := range []struct {
		name string
		body []byte
	}{
		{name: "go.mod", body: goMod},
		{name: "main.go", body: []byte("package main\nfunc main() {}\n")},
	} {
		writer, err := zipWriter.Create(module + "@" + version + "/" + entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "bin")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	proxyURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(proxyRoot)}).String()
	command := exec.Command("go", "install", module+"@"+version)
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-buildvcs=false", "GOBIN="+output, "GOPROXY="+proxyURL, "GOSUMDB=off", "GOWORK=off")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build versioned binary through local module proxy: %v: %s", err, raw)
	}
	name := "inspection"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(output, name)
}

func TestInspectExecutableRejectsManifestVersionConflict(t *testing.T) {
	path := buildVersionedInspectionBinary(t, "v1.2.3")
	source, err := InspectExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if source.Version != "v1.2.3" {
		t.Fatalf("build-info version = %q, want v1.2.3", source.Version)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version": 1,
		"kind":           "go-binary",
		"version":        "v9.9.9",
		"platform":       map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "cgo": "0"},
		"artifact":       map[string]any{"size_bytes": info.Size(), "sha256": source.Digest},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(path+".manifest.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = InspectExecutable(path)
	if err == nil {
		t.Fatal("conflicting build-info and manifest versions were accepted")
	}
	if !errors.Is(err, ErrTrust) || !strings.Contains(err.Error(), "version") {
		t.Fatalf("conflicting build-info and manifest versions = %v, want trust conflict", err)
	}
}

func TestInspectExecutableContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectExecutableContext(ctx, "unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inspection = %v", err)
	}
}
