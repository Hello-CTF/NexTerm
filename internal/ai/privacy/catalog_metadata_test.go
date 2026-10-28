package privacy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setFixtureFile(installation Installation, files map[string][]byte, name, content string) {
	data := []byte(content)
	hash := sha256.Sum256(data)
	files[name] = data
	installation.Files[name] = ModelFile{Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
}

// A release whose own metadata this runtime cannot load must not be published as an
// installation even when every file matches its manifest and Resolve was bypassed.
func TestCatalogDownloadRejectsIncompatibleMetadata(t *testing.T) {
	installation, files := fixtureInstallation(t, INT8)
	setFixtureFile(installation, files, "config.json", "incompatible config")
	fixture := newCatalogFixture(t, installation, files)
	manifest, err := fixture.catalog.Manifest(t.Context(), installation.ModelRelease, INT8)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	_, err = fixture.catalog.Download(t.Context(), root, manifest, nil, nil)
	if !errors.Is(err, errIncompatibleRelease) {
		t.Fatalf("err=%v, want incompatible release", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected download left %d entries in the model root", len(entries))
	}
	if _, statErr := os.Stat(filepath.Join(root, installation.Revision+"-"+string(INT8))); !os.IsNotExist(statErr) {
		t.Fatalf("incompatible installation was published: %v", statErr)
	}
}

// Metadata files are small by contract; a manifest that announces a huge one is
// refused before anything is downloaded.
func TestCatalogManifestBoundsMetadataSizes(t *testing.T) {
	installation, files := fixtureInstallation(t, INT8)
	for _, name := range []string{"config.json", "tokenizer.json", "labels.json"} {
		bad := installation
		bad.Files = map[string]ModelFile{}
		for key, value := range installation.Files {
			bad.Files[key] = value
		}
		entry := bad.Files[name]
		entry.Size = maxMetadataFile(name) + 1
		bad.Files[name] = entry
		fixture := newCatalogFixture(t, bad, files)
		if _, err := fixture.catalog.Manifest(t.Context(), installation.ModelRelease, INT8); !errors.Is(err, errManifestEntry) {
			t.Fatalf("%s: err=%v, want invalid manifest entry", name, err)
		}
	}
}
