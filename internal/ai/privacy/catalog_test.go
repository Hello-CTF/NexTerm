package privacy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fixtureInstallation(t *testing.T, precision Precision) (Installation, map[string][]byte) {
	t.Helper()
	installation := Installation{ModelRelease: ModelRelease{Version: "v0.1.3", Revision: strings.Repeat("a", 40)}, Precision: precision, Files: map[string]ModelFile{}}
	files := map[string][]byte{}
	for _, name := range requiredModelFiles(precision) {
		data := []byte("fixture: " + name)
		hash := sha256.Sum256(data)
		files[name] = data
		installation.Files[name] = ModelFile{Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
	}
	return installation, files
}

func writeFixtureInstallation(t *testing.T, root string, installation Installation, files map[string][]byte) string {
	t.Helper()
	directory := filepath.Join(root, installation.Revision+"-"+string(installation.Precision))
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(installation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "installation.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return directory
}

type catalogFixture struct {
	catalog *Catalog
	mu      sync.Mutex
	paths   []string
}

func newCatalogFixture(t *testing.T, installation Installation, files map[string][]byte) *catalogFixture {
	t.Helper()
	fixture := &catalogFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.paths = append(fixture.paths, r.URL.Path)
		fixture.mu.Unlock()
		if r.URL.Path == "/api/models/"+modelRepository+"/refs" {
			fmt.Fprintf(w, `{"tags":[{"name":"v0.1.2","targetCommit":%q},{"name":%q,"targetCommit":%q},{"name":"v1.0.0","targetCommit":%q},{"name":"v2.0.0-rc.1","targetCommit":%q},{"name":"not-semver","targetCommit":%q}]}`, strings.Repeat("b", 40), installation.Version, installation.Revision, strings.Repeat("e", 40), strings.Repeat("c", 40), strings.Repeat("d", 40))
			return
		}
		prefix := "/" + modelRepository + "/resolve/" + installation.Revision + "/" + string(installation.Precision) + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if name == "CHECKSUMS.json" {
			_ = json.NewEncoder(w).Encode(installation.Files)
			return
		}
		data, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	fixture.catalog = &Catalog{baseURL: server.URL, client: server.Client()}
	return fixture
}

func TestCatalogUsesLatestStableImmutableRevisionAndChecksums(t *testing.T) {
	installation, files := fixtureInstallation(t, INT8)
	fixture := newCatalogFixture(t, installation, files)
	release, err := fixture.catalog.Latest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if release != installation.ModelRelease {
		t.Fatalf("release=%+v", release)
	}
	manifest, err := fixture.catalog.Manifest(t.Context(), release, INT8)
	if err != nil {
		t.Fatal(err)
	}
	var progress DownloadProgress
	directory, err := fixture.catalog.Download(t.Context(), t.TempDir(), manifest, func(next DownloadProgress) { progress = next }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstallation(t.Context(), directory, manifest); err != nil {
		t.Fatal(err)
	}
	if progress.Downloaded != progress.Total || progress.Total == 0 {
		t.Fatalf("incomplete progress: %+v", progress)
	}
	fixture.mu.Lock()
	paths := append([]string(nil), fixture.paths...)
	fixture.mu.Unlock()
	for _, path := range paths {
		if strings.Contains(path, "/resolve/") && !strings.Contains(path, "/resolve/"+release.Revision+"/") {
			t.Fatalf("mutable download path %q", path)
		}
	}
}

func TestCatalogRejectsCorruptDownloadsAndCleansPartialFiles(t *testing.T) {
	installation, files := fixtureInstallation(t, FP16)
	files[modelFileName(FP16)] = []byte("corrupted")
	fixture := newCatalogFixture(t, installation, files)
	root := t.TempDir()
	if _, err := fixture.catalog.Download(t.Context(), root, installation, nil, nil); err == nil {
		t.Fatal("corrupt model accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("partial installation was published or leaked")
	}
}

func TestCatalogCancellationDoesNotPublish(t *testing.T) {
	installation, files := fixtureInstallation(t, INT8)
	fixture := newCatalogFixture(t, installation, files)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	_, err := fixture.catalog.Download(ctx, root, installation, func(DownloadProgress) { cancel() }, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("canceled installation was published or leaked")
	}
}

func TestCatalogRedownloadRepairsCorruptExistingInstallation(t *testing.T) {
	installation, files := fixtureInstallation(t, INT8)
	fixture := newCatalogFixture(t, installation, files)
	root := t.TempDir()
	directory := writeFixtureInstallation(t, root, installation, files)
	if err := os.WriteFile(filepath.Join(directory, modelFileName(INT8)), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	repaired, err := fixture.catalog.Download(t.Context(), root, installation, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstallation(t.Context(), repaired, installation); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogManifestRejectsMissingOrUnsafeEntries(t *testing.T) {
	for _, change := range []func(*Installation){
		func(i *Installation) { delete(i.Files, "config.json") },
		func(i *Installation) {
			entry := i.Files["config.json"]
			entry.Size = maxModelFile + 1
			i.Files["config.json"] = entry
		},
		func(i *Installation) {
			entry := i.Files["config.json"]
			entry.SHA256 = "invalid"
			i.Files["config.json"] = entry
		},
	} {
		installation, files := fixtureInstallation(t, INT8)
		change(&installation)
		fixture := newCatalogFixture(t, installation, files)
		if _, err := fixture.catalog.Manifest(t.Context(), installation.ModelRelease, INT8); err == nil {
			t.Fatal("unsafe manifest accepted")
		}
	}
	installation, files := fixtureInstallation(t, INT8)
	installation.Files["../../outside"] = installation.Files["config.json"]
	fixture := newCatalogFixture(t, installation, files)
	manifest, err := fixture.catalog.Manifest(t.Context(), installation.ModelRelease, INT8)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest.Files["../../outside"]; ok {
		t.Fatal("remote path retained")
	}
}
