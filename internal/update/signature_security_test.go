package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSignedReleaseAssetSizeBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		asset string
		extra int
	}{
		{name: "manifest at limit", asset: checksumsAssetName},
		{name: "manifest at limit plus one", asset: checksumsAssetName, extra: 1},
		{name: "signature at limit", asset: checksumSignatureAssetName},
		{name: "signature at limit plus one", asset: checksumSignatureAssetName, extra: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := newTestMinisignKey(20)
			fixture := linuxFixture(t, "0.3.0", false)
			switch test.asset {
			case checksumsAssetName:
				fixture.checksums = manifestOfSize(t, fixture.checksums, maxChecksumManifestSize+test.extra)
				fixture.sign(key, "0.3.0")
			case checksumSignatureAssetName:
				fixture.sign(key, "0.3.0")
				fixture.signature = signatureOfSize(t, fixture.signature, maxMinisignSignatureSize+test.extra)
			default:
				t.Fatalf("unexpected asset %q", test.asset)
			}

			trust, err := parseMinisignPublicKey(key.encodedPublic)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyMinisign([]byte(fixture.checksums), []byte(fixture.signature), trust, "NexTerm 0.3.0 SHA256SUMS"); err != nil {
				t.Fatalf("test input must be cryptographically valid: %v", err)
			}
			server := newObservedReleaseServer(t, fixture, nil)
			executable := filepath.Join(t.TempDir(), "nexterm-desktop")
			if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			restartCalled := false
			manager := &Manager{
				CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
				ExePath: executable, APIURL: server.server.URL + "/releases",
				MinisignPublicKey: key.publicKeyFile(), RequireSignedChecksums: true,
				RestartFunc: func(context.Context, string) error {
					restartCalled = true
					return nil
				},
			}

			status := manager.Check(context.Background())
			wantAvailable := test.extra == 0
			if status.Available != wantAvailable {
				t.Fatalf("status = %+v, want Available %v", status, wantAvailable)
			}
			if !wantAvailable {
				if status.UnavailableReason == "" {
					t.Fatal("oversized asset must report an unavailable reason")
				}
				result, err := manager.Install(context.Background(), "")
				if err != nil {
					t.Fatalf("install should stop during its check: %v", err)
				}
				if result.Installed {
					t.Fatalf("result = %+v", result)
				}
			}
			if requests := server.artifactRequests.Load(); requests != 0 {
				t.Fatalf("artifact requests = %d, want 0; applyInstall must remain unreachable", requests)
			}
			if restartCalled {
				t.Fatal("restart must not be called")
			}
			content, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "old-binary" {
				t.Fatalf("executable must not be replaced, content = %q", content)
			}
		})
	}
}

func TestInstallResolveAssetRevalidatesSignature(t *testing.T) {
	tests := []struct {
		name       string
		failStatus bool
	}{
		{name: "bad signature"},
		{name: "signature not found", failStatus: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := newTestMinisignKey(21)
			fixture := linuxFixture(t, "0.3.0", false)
			fixture.sign(key, "0.3.0")
			validSignature := fixture.signature
			badSignature := key.sign([]byte("another manifest"), "NexTerm 0.3.0 SHA256SUMS")
			server := newObservedReleaseServer(t, fixture, func(response http.ResponseWriter, attempt int64) {
				if attempt <= 2 {
					_, _ = response.Write([]byte(validSignature))
					return
				}
				if test.failStatus {
					response.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = response.Write([]byte(badSignature))
			})
			executable := filepath.Join(t.TempDir(), "nexterm-desktop")
			if err := os.WriteFile(executable, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			restartCalled := false
			manager := &Manager{
				CurrentVersion: "0.2.2", GOOS: "linux", GOARCH: "amd64", CanInstall: true,
				ExePath: executable, APIURL: server.server.URL + "/releases",
				MinisignPublicKey: key.publicKeyFile(), RequireSignedChecksums: true,
				RestartFunc: func(context.Context, string) error {
					restartCalled = true
					return nil
				},
			}

			if status := manager.Check(context.Background()); !status.Available {
				t.Fatalf("initial check = %+v", status)
			}
			result, err := manager.Install(context.Background(), "")
			if err == nil {
				t.Fatalf("resolveAsset must reject the changed signature, result = %+v", result)
			}
			if result.Installed {
				t.Fatalf("result = %+v", result)
			}
			if requests := server.signatureRequests.Load(); requests != 3 {
				t.Fatalf("signature requests = %d, want initial Check, Install Check, then resolveAsset", requests)
			}
			if requests := server.artifactRequests.Load(); requests != 0 {
				t.Fatalf("artifact requests = %d, want 0; applyInstall must remain unreachable", requests)
			}
			if restartCalled {
				t.Fatal("restart must not be called")
			}
			content, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "old-binary" {
				t.Fatalf("executable must not be replaced, content = %q", content)
			}
		})
	}
}

func manifestOfSize(t *testing.T, manifest string, size int) string {
	t.Helper()
	if size < len(manifest) {
		t.Fatalf("manifest size %d is smaller than base %d", size, len(manifest))
	}
	return manifest + strings.Repeat("\n", size-len(manifest))
}

func signatureOfSize(t *testing.T, signature string, size int) string {
	t.Helper()
	if size < len(signature) {
		t.Fatalf("signature size %d is smaller than base %d", size, len(signature))
	}
	newline := strings.IndexByte(signature, '\n')
	if newline < 0 {
		t.Fatal("signature has no untrusted comment newline")
	}
	return signature[:newline] + strings.Repeat(" ", size-len(signature)) + signature[newline:]
}

type observedReleaseServer struct {
	server            *httptest.Server
	artifactRequests  atomic.Int64
	signatureRequests atomic.Int64
}

func newObservedReleaseServer(t *testing.T, fixture *releaseFixture, signatureOverride func(http.ResponseWriter, int64)) *observedReleaseServer {
	t.Helper()
	observed := &observedReleaseServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(fixture.releases)
	})
	mux.HandleFunc("/assets/"+checksumsAssetName, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(fixture.checksums))
	})
	mux.HandleFunc("/assets/"+checksumSignatureAssetName, func(response http.ResponseWriter, _ *http.Request) {
		attempt := observed.signatureRequests.Add(1)
		if signatureOverride != nil {
			signatureOverride(response, attempt)
			return
		}
		if fixture.signature == "" {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = response.Write([]byte(fixture.signature))
	})
	mux.HandleFunc("/assets/"+fixture.archiveName, func(response http.ResponseWriter, _ *http.Request) {
		observed.artifactRequests.Add(1)
		_, _ = response.Write(fixture.archive)
	})
	observed.server = httptest.NewServer(mux)
	t.Cleanup(observed.server.Close)
	for index := range fixture.releases {
		for assetIndex := range fixture.releases[index].Assets {
			asset := &fixture.releases[index].Assets[assetIndex]
			asset.BrowserDownloadURL = observed.server.URL + "/assets/" + asset.Name
		}
	}
	return observed
}
