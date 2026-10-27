package main

import (
	"crypto/sha256"
	"encoding/base64"
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
		Protocol int ` + "`json:\"protocol\"`" + `
	}{Protocol: %d})
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
		"mkdir -p " + shellQuote(filepath.Join(base, ".github", "scripts")),
		"cp " + shellQuote(filepath.Join(root, ".github", "native-matrix.json")) + " " + shellQuote(filepath.Join(base, ".github", "native-matrix.json")),
		"cp " + shellQuote(filepath.Join(root, ".github", "scripts", "helper-matrix.mjs")) + " " + shellQuote(filepath.Join(base, ".github", "scripts", "helper-matrix.mjs")),
		"cp " + shellQuote(filepath.Join(root, ".github", "scripts", "helper-metadata.mjs")) + " " + shellQuote(filepath.Join(base, ".github", "scripts", "helper-metadata.mjs")),
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
		Protocol int `json:"protocol"`
	}
	if err := json.Unmarshal([]byte(metadataRaw.stdout), &metadata); err != nil {
		return fmt.Errorf("parse helper metadata: %w", err)
	}
	if metadata.Protocol <= 0 {
		return fmt.Errorf("helper metadata is incomplete: %s", metadataRaw.stdout)
	}
	helperCommand := filepath.Join(base, "cmd", "nexterm-helper")
	if err := os.MkdirAll(helperCommand, 0o755); err != nil {
		return err
	}
	mainSource := fmt.Sprintf(helperMetadataMain, metadata.Protocol)
	if err := os.WriteFile(filepath.Join(helperCommand, "main.go"), []byte(mainSource), 0o644); err != nil {
		return err
	}
	helperRoot := filepath.Join(base, "target", "go-build")
	for _, target := range helperFixtureTargets {
		buildDir := filepath.Join(base, "helper-build", target.goos+"-"+target.arch)
		if err := os.MkdirAll(buildDir, 0o755); err != nil {
			return err
		}
		helper, err := buildStubBinary(buildDir, "package main\n\nfunc main() {}\n", target.goos, target.arch, "0")
		if err != nil {
			return fmt.Errorf("build fixture helper %s/%s: %w", target.goos, target.arch, err)
		}
		destination := filepath.Join(helperRoot, target.name)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := copyFile(helper, destination, 0o755); err != nil {
			return err
		}
		if err := writeHelperFixtureManifest(destination, target.goos, target.arch, target.representative, version, metadata.Protocol); err != nil {
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

type helperFixtureTarget struct {
	goos           string
	arch           string
	name           string
	representative bool
}

var helperFixtureTargets = []helperFixtureTarget{
	{goos: "linux", arch: "amd64", name: "nexterm-helper-linux-amd64", representative: true},
	{goos: "linux", arch: "arm64", name: "nexterm-helper-linux-arm64"},
	{goos: "linux", arch: "armv7", name: "nexterm-helper-linux-armv7"},
	{goos: "linux", arch: "riscv64", name: "nexterm-helper-linux-riscv64"},
	{goos: "linux", arch: "loong64", name: "nexterm-helper-linux-loong64"},
	{goos: "darwin", arch: "amd64", name: "nexterm-helper-darwin-amd64"},
	{goos: "darwin", arch: "arm64", name: "nexterm-helper-darwin-arm64", representative: true},
	{goos: "windows", arch: "amd64", name: "nexterm-helper-windows-amd64.exe", representative: true},
	{goos: "windows", arch: "arm64", name: "nexterm-helper-windows-arm64.exe"},
}

func copyFile(source, destination string, mode os.FileMode) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, contents, mode)
}

func writeHelperFixtureManifest(file, goos, arch string, representative bool, version string, protocol int) error {
	if err := writeBinaryManifest(file, "helper", goos, arch, "0", version, "unknown"); err != nil {
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
	if representative {
		// Representative entries are synthetic validator fixtures, not platform evidence.
		binaryBytes, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(binaryBytes)
		manifest["protocol"] = protocol
		manifest["sha256"] = fmt.Sprintf("%x", digest)
		manifest["binary_metadata_verified"] = true
		manifest["metadata_host"] = map[string]string{"os": goos, "arch": arch}
	} else {
		manifest["binary_metadata_verified"] = false
		manifest["metadata_status"] = "not-run"
		manifest["cross_compiled"] = true
		manifest["untested"] = true
	}
	if arch == "armv7" {
		manifest["platform"].(map[string]interface{})["goarm"] = "7"
	}
	updated, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(updated, '\n'), 0o644)
}

const syntheticDesktopUpdaterPublicKey = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"

func syntheticDesktopUpdaterPublicKeyVariant() string {
	decoded, err := base64.StdEncoding.DecodeString(syntheticDesktopUpdaterPublicKey)
	if err != nil {
		panic(err)
	}
	decoded[len(decoded)-1] ^= 0xff
	return base64.StdEncoding.EncodeToString(decoded)
}

func editDesktopBinaryManifest(binary string, edit func(map[string]interface{})) error {
	manifestPath := binary + ".manifest.json"
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	edit(manifest)
	updated, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(updated, '\n'), 0o644)
}

func addDesktopReuseEvidence(binary, version string) error {
	digest := sha256.Sum256([]byte(syntheticDesktopUpdaterPublicKey))
	return editDesktopBinaryManifest(binary, func(manifest map[string]interface{}) {
		manifest["source_ref"] = "refs/tags/v" + version
		manifest["updater_public_key_sha256"] = fmt.Sprintf("%x", digest)
	})
}

func cloneDesktopBinary(source, directory string) (string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	destination := filepath.Join(directory, filepath.Base(source))
	if err := copyFile(source, destination, 0o755); err != nil {
		return "", err
	}
	if err := copyFile(source+".manifest.json", destination+".manifest.json", 0o644); err != nil {
		return "", err
	}
	if err := writeStubPrivacyRuntime(destination, runtime.GOOS, runtime.GOARCH); err != nil {
		return "", err
	}
	return destination, nil
}

func runDesktopPackage(base, binary, version, goos, goarch string, extraEnv ...string) cmdResult {
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
		"NEXTERM_RELEASE_REF=refs/tags/v"+version,
		"NEXTERM_MINISIGN_PUBLIC_KEY="+syntheticDesktopUpdaterPublicKey,
		"NEXTERM_MACOS_SIGN_IDENTITY=-",
		"NEXTERM_MACOS_NOTARY_KEY_PATH=",
		"NEXTERM_MACOS_NOTARY_KEY_ID=",
		"NEXTERM_MACOS_NOTARY_ISSUER=",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
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
		if err := addDesktopReuseEvidence(binary, version); err != nil {
			fatalf("%v", err)
		}
	}

	good := runDesktopPackage(repo, goodBinary, version, goos, goarch,
		"NEXTERM_MINISIGN_PUBLIC_KEY=untrusted comment: synthetic test key\n"+syntheticDesktopUpdaterPublicKey+"\n")
	_, goodAssertions, _ := readArtifactReport(goodBinary + ".artifact.json")
	packages, readPackagesErr := os.ReadDir(filepath.Join(repo, "release-assets"))
	packageEvidence := map[string]map[string]string{}
	for _, pkg := range packages {
		if strings.HasSuffix(pkg.Name(), ".artifact.json") {
			_, assertions, _ := readArtifactReport(filepath.Join(repo, "release-assets", pkg.Name()))
			packageEvidence[pkg.Name()] = assertions
		}
	}
	c.check("desktop package accepts exact embedded assets and normalized updater evidence",
		good.rc == 0 && goodAssertions["embedded-index"] == "passed" && goodAssertions["embedded-script"] == "passed" && readPackagesErr == nil && len(packages) > 0,
		fmt.Sprintf("rc=%d assertions=%v package_assertions=%v packages=%d stderr=%q", good.rc, goodAssertions, packageEvidence, len(packages), good.stderr))

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

	for _, scenario := range []struct {
		name       string
		want       string
		edit       func(map[string]interface{})
		helperName string
		helperEdit func(map[string]interface{})
		tamper     bool
		version    string
		env        []string
	}{
		{
			name:       "helper protocol mismatch",
			want:       "protocol",
			helperName: "nexterm-helper-linux-amd64",
			helperEdit: func(manifest map[string]interface{}) { manifest["protocol"] = 1 },
		},
		{
			name: "updater public key mismatch",
			want: "updater public key SHA-256",
			env:  []string{"NEXTERM_MINISIGN_PUBLIC_KEY=" + syntheticDesktopUpdaterPublicKeyVariant()},
		},
		{
			name: "commit mismatch",
			want: "commit",
			edit: func(manifest map[string]interface{}) { manifest["commit"] = strings.Repeat("0", 40) },
		},
		{
			name: "manifest ref mismatch",
			want: "source_ref",
			edit: func(manifest map[string]interface{}) { manifest["source_ref"] = "refs/heads/master" },
		},
		{
			name: "current ref mismatch",
			want: "NEXTERM_RELEASE_REF",
			env:  []string{"NEXTERM_RELEASE_REF=refs/heads/master"},
		},
		{
			name: "missing updater public key evidence",
			want: "updater public key SHA-256",
			edit: func(manifest map[string]interface{}) { delete(manifest, "updater_public_key_sha256") },
		},
		{
			name:   "binary tamper",
			want:   "binary sha256 mismatch",
			tamper: true,
		},
		{
			name:    "master placeholder version",
			want:    "release version",
			version: "0.0.0",
		},
	} {
		binary, err := cloneDesktopBinary(goodBinary, filepath.Join(base, "reuse-"+strings.ReplaceAll(scenario.name, " ", "-")))
		if err != nil {
			fatalf("%v", err)
		}
		if scenario.edit != nil {
			if err := editDesktopBinaryManifest(binary, scenario.edit); err != nil {
				fatalf("%v", err)
			}
		}
		if scenario.tamper {
			contents, err := os.ReadFile(binary)
			if err != nil {
				fatalf("%v", err)
			}
			if err := os.WriteFile(binary, append(contents, 0), 0o755); err != nil {
				fatalf("%v", err)
			}
		}
		scenarioVersion := scenario.version
		if scenarioVersion == "" {
			scenarioVersion = version
		}
		var helperManifestPath string
		var helperManifestRaw []byte
		if scenario.helperName != "" {
			helperManifestPath = filepath.Join(repo, "target", "go-build", scenario.helperName+".manifest.json")
			helperManifestRaw, err = os.ReadFile(helperManifestPath)
			if err != nil {
				fatalf("%v", err)
			}
			var helperManifest map[string]interface{}
			if err := json.Unmarshal(helperManifestRaw, &helperManifest); err != nil {
				fatalf("%v", err)
			}
			scenario.helperEdit(helperManifest)
			updated, err := json.MarshalIndent(helperManifest, "", "  ")
			if err != nil {
				fatalf("%v", err)
			}
			if err := os.WriteFile(helperManifestPath, append(updated, '\n'), 0o644); err != nil {
				fatalf("%v", err)
			}
		}
		assets := filepath.Join(repo, "release-assets")
		if err := os.RemoveAll(assets); err != nil {
			fatalf("%v", err)
		}
		result := runDesktopPackage(repo, binary, scenarioVersion, goos, goarch, scenario.env...)
		if helperManifestPath != "" {
			if err := os.WriteFile(helperManifestPath, helperManifestRaw, 0o644); err != nil {
				fatalf("%v", err)
			}
		}
		_, statErr := os.Stat(assets)
		c.check("desktop package rejects "+scenario.name,
			result.rc != 0 && strings.Contains(result.stderr, scenario.want) && os.IsNotExist(statErr),
			fmt.Sprintf("rc=%d no_artifacts=%t stderr=%q", result.rc, os.IsNotExist(statErr), result.stderr))
	}
	return c.results
}
