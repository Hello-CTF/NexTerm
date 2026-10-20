package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const desktopEmbedMain = `package main

import "embed"

//go:embed index.html assets
var files embed.FS

func main() {
	_, _ = files.ReadFile("index.html")
}
`

const helperMetadataMain = `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "--metadata" {
		fmt.Fprintln(os.Stderr, "fixture helper: expected --metadata")
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Protocol int      ` + "`json:\"protocol\"`" + `
		Features []string ` + "`json:\"features\"`" + `
	}{Protocol: %d, Features: []string{%s}})
}
`

func buildDesktopDistStub(directory, distRoot string, tamperIndex bool) (string, error) {
	index, err := os.ReadFile(filepath.Join(distRoot, "index.html"))
	if err != nil {
		return "", err
	}
	if tamperIndex {
		index[0] = 'X'
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), index, 0o644); err != nil {
		return "", err
	}
	assetsRoot := filepath.Join(distRoot, "assets")
	err = filepath.Walk(assetsRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".js") {
			return nil
		}
		relative, err := filepath.Rel(distRoot, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		destination := filepath.Join(directory, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, contents, 0o644)
	})
	if err != nil {
		return "", err
	}
	return buildStubBinary(directory, desktopEmbedMain, runtime.GOOS, runtime.GOARCH, "1")
}

func prepareDesktopPackageRepo(root, base, version string) error {
	script := strings.Join([]string{
		"cp -R " + shellQuote(filepath.Join(root, "scripts")) + " " + shellQuote(filepath.Join(base, "scripts")),
		"cp -R " + shellQuote(filepath.Join(root, "public")) + " " + shellQuote(filepath.Join(base, "public")),
		"cp " + shellQuote(filepath.Join(root, "wails.json")) + " " + shellQuote(filepath.Join(root, "package.json")) + " " + shellQuote(filepath.Join(root, "go.mod")) + " " + shellQuote(filepath.Join(root, "go.sum")) + " " + shellQuote(filepath.Join(root, "LICENSE")) + " " + shellQuote(base),
	}, "\n")
	result := runScript(script, base, nil)
	if result.rc != 0 {
		return fmt.Errorf("copy desktop package fixture: %s", result.stderr)
	}
	dist := filepath.Join(base, "dist")
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		return err
	}
	index := []byte("<html><script src=\"/assets/app-a1b2c3.js\"></script></html>\n")
	if err := os.WriteFile(filepath.Join(dist, "index.html"), index, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app-a1b2c3.js"), []byte("console.log(\"desktop evidence\");\n"), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(base, "target"), 0o755); err != nil {
		return err
	}
	// The package-only path validates the helper protocol before packaging. Keep
	// this temporary repository self-contained so the evidence check does not
	// depend on production build leftovers in the caller's target directory.
	metadataCmd := exec.Command("go", "run", "-mod=readonly", "./cmd/nexterm-helper", "--metadata")
	metadataCmd.Dir = root
	metadataRaw := runCmd(metadataCmd)
	if metadataRaw.rc != 0 {
		return fmt.Errorf("read helper metadata: %s", metadataRaw.stderr)
	}
	var metadata struct {
		Protocol int      `json:"protocol"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal([]byte(metadataRaw.stdout), &metadata); err != nil {
		return fmt.Errorf("parse helper metadata: %w", err)
	}
	if metadata.Protocol <= 0 || len(metadata.Features) == 0 {
		return fmt.Errorf("helper metadata is incomplete: %s", metadataRaw.stdout)
	}
	helperCommand := filepath.Join(base, "cmd", "nexterm-helper")
	if err := os.MkdirAll(helperCommand, 0o755); err != nil {
		return err
	}
	featuresJSON, err := json.Marshal(metadata.Features)
	if err != nil {
		return err
	}
	featuresLiteral := strings.TrimSuffix(strings.TrimPrefix(string(featuresJSON), "["), "]")
	mainSource := fmt.Sprintf(helperMetadataMain, metadata.Protocol, featuresLiteral)
	if err := os.WriteFile(filepath.Join(helperCommand, "main.go"), []byte(mainSource), 0o644); err != nil {
		return err
	}
	helperRoot := filepath.Join(base, "target", "go-build")
	for _, arch := range []string{"amd64", "arm64"} {
		buildDir := filepath.Join(base, "helper-build", arch)
		if err := os.MkdirAll(buildDir, 0o755); err != nil {
			return err
		}
		helper, err := buildStubBinary(buildDir, "package main\n\nfunc main() {}\n", "linux", arch, "0")
		if err != nil {
			return fmt.Errorf("build fixture helper %s: %w", arch, err)
		}
		destination := filepath.Join(helperRoot, "nexterm-helper-linux-"+arch)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := copyFile(helper, destination, 0o755); err != nil {
			return err
		}
		if err := writeHelperFixtureManifest(destination, arch, version, metadata.Protocol, metadata.Features); err != nil {
			return err
		}
	}
	manifestScript := `import fs from "node:fs";
import { createDistManifest } from "./scripts/lib/build-manifest.mjs";
const manifest = createDistManifest(process.env.DIST_ROOT, { version: process.env.DIST_VERSION, commit: process.env.DIST_COMMIT });
fs.writeFileSync(process.env.DIST_MANIFEST, JSON.stringify(manifest));`
	cmd := exec.Command("node", "--input-type=module", "-e", manifestScript)
	cmd.Dir = base
	cmd.Env = append(os.Environ(),
		"DIST_ROOT="+dist,
		"DIST_VERSION="+version,
		"DIST_COMMIT=unknown",
		"DIST_MANIFEST="+filepath.Join(base, "target", "dist-manifest.json"),
	)
	result = runCmd(cmd)
	if result.rc != 0 {
		return fmt.Errorf("create desktop dist manifest: %s", result.stderr)
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, contents, mode)
}

func writeHelperFixtureManifest(file, arch, version string, protocol int, features []string) error {
	if err := writeBinaryManifest(file, "helper", "linux", arch, "0", version, "unknown"); err != nil {
		return err
	}
	manifestPath := file + ".manifest.json"
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	manifest["protocol"] = protocol
	manifest["features"] = features
	manifest["binary_metadata_verified"] = true
	updated, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(updated, '\n'), 0o644)
}

func runDesktopPackage(base, binary, version, goos, goarch string) cmdResult {
	args := []string{
		"node", filepath.Join(base, "scripts", "build.mjs"), "desktop",
		"--release", "--package", "--package-only",
		"--consume-dist=" + filepath.Join(base, "dist"),
		"--dist-manifest=" + filepath.Join(base, "target", "dist-manifest.json"),
		"--os=" + goos, "--arch=" + goarch,
		"--out=" + binary,
		"--assets-dir=" + filepath.Join(base, "release-assets"),
		"--require-evidence",
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = base
	cmd.Env = append(os.Environ(),
		"SOURCE_DATE_EPOCH=1",
		"NEXTERM_RELEASE_VERSION="+version,
		"NEXTERM_MACOS_SIGN_IDENTITY=-",
		"NEXTERM_MACOS_NOTARY_KEY_PATH=",
		"NEXTERM_MACOS_NOTARY_KEY_ID=",
		"NEXTERM_MACOS_NOTARY_ISSUER=",
	)
	return runCmd(cmd)
}

func runDesktopDistChecks(root, version, goos, goarch string) []checkResult {
	c := &collector{}
	base, err := os.MkdirTemp("", "desktop-dist-evidence-")
	if err != nil {
		fatalf("%v", err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(base)
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		fatalf("%v", err)
	}
	if err := prepareDesktopPackageRepo(root, repo, version); err != nil {
		fatalf("%v", err)
	}

	goodDir := filepath.Join(base, "good")
	missingDir := filepath.Join(base, "missing")
	tamperedDir := filepath.Join(base, "tampered")
	for _, directory := range []string{goodDir, missingDir, tamperedDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			fatalf("%v", err)
		}
	}
	goodBinary, err := buildDesktopDistStub(goodDir, filepath.Join(repo, "dist"), false)
	if err != nil {
		fatalf("%v", err)
	}
	missingBinary, err := buildStubBinary(missingDir, "package main\n\nfunc main() {}\n", goos, goarch, "1")
	if err != nil {
		fatalf("%v", err)
	}
	tamperedBinary, err := buildDesktopDistStub(tamperedDir, filepath.Join(repo, "dist"), true)
	if err != nil {
		fatalf("%v", err)
	}
	for _, binary := range []string{goodBinary, missingBinary, tamperedBinary} {
		if err := writeBinaryManifest(binary, "desktop", goos, goarch, "1", version, "unknown"); err != nil {
			fatalf("%v", err)
		}
	}

	good := runDesktopPackage(repo, goodBinary, version, goos, goarch)
	_, goodAssertions, _ := readArtifactReport(goodBinary + ".artifact.json")
	packages, readPackagesErr := os.ReadDir(filepath.Join(repo, "release-assets"))
	c.check("desktop package accepts exact embedded index and scripts",
		good.rc == 0 && goodAssertions["embedded-index"] == "passed" && goodAssertions["embedded-script"] == "passed" && readPackagesErr == nil && len(packages) > 0,
		fmt.Sprintf("rc=%d assertions=%v packages=%d stderr=%q", good.rc, goodAssertions, len(packages), good.stderr))

	missing := runDesktopPackage(repo, missingBinary, version, goos, goarch)
	_, missingAssertions, _ := readArtifactReport(missingBinary + ".artifact.json")
	c.check("desktop package rejects missing embedded index and scripts",
		missing.rc != 0 && missingAssertions["embedded-index"] == "failed" && missingAssertions["embedded-script"] == "failed",
		fmt.Sprintf("rc=%d assertions=%v stderr=%q", missing.rc, missingAssertions, missing.stderr))

	tampered := runDesktopPackage(repo, tamperedBinary, version, goos, goarch)
	_, tamperedAssertions, _ := readArtifactReport(tamperedBinary + ".artifact.json")
	c.check("desktop package rejects a tampered embedded index",
		tampered.rc != 0 && tamperedAssertions["embedded-index"] == "failed",
		fmt.Sprintf("rc=%d assertions=%v stderr=%q", tampered.rc, tamperedAssertions, tampered.stderr))
	return c.results
}
