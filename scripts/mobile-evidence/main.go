// Command mobile-evidence writes the release evidence report for an Android APK
// or iOS IPA and fails when any assertion fails:
//
//	go run ./scripts/mobile-evidence <package> <mobile-android|mobile-ios>
//
// The report is written to <package>.artifact.json. The ZIP central directory
// is validated before any entry content is read; iOS packages are then checked
// for the release version, device-only Mach-O slices, an App Store provisioning
// profile (via openssl cms) and an executable bundle binary; Android packages
// are checked with apksigner and aapt2 when the SDK tools are available.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	mib                     = int64(1024 * 1024)
	gib                     = 1024 * mib
	maxToolOutputBytes      = 16 * mib
	maxPlistOrProfileBytes  = 16 * mib
	maxExecutableBytes      = 512 * mib
	iosInfoPlist            = "Payload/nexterm.app/Info.plist"
	iosExecutable           = "Payload/nexterm.app/nexterm"
	iosProvisioningProfile  = "Payload/nexterm.app/embedded.mobileprovision"
	iosDevicePlatform       = 2
	androidVersionCodeLimit = 2100000000
)

type assertion struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type report struct {
	SchemaVersion   int         `json:"schema_version"`
	ID              string      `json:"id"`
	Kind            string      `json:"kind"`
	Platform        platform    `json:"platform"`
	Version         string      `json:"version"`
	Commit          string      `json:"commit"`
	SourceDateEpoch int64       `json:"source_date_epoch"`
	Artifact        artifact    `json:"artifact"`
	Format          format      `json:"format"`
	Assertions      []assertion `json:"assertions"`
	Status          string      `json:"status"`
}

type platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	CGO  string `json:"cgo"`
}

type artifact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Stripped  bool   `json:"stripped"`
}

type format struct {
	Format       string `json:"format"`
	Architecture string `json:"architecture"`
	Static       bool   `json:"static"`
}

type limits struct {
	maxPackage, maxTotal, maxEntry int64
	toolTimeout                    time.Duration
	overrides                      map[string]time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) != 2 || (args[1] != "mobile-android" && args[1] != "mobile-ios") {
		fmt.Fprintln(stderr, "usage: go run ./scripts/mobile-evidence <package> <mobile-android|mobile-ios>")
		return 1
	}
	id := args[1]
	file, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		fmt.Fprintf(stderr, "mobile package does not exist: %s\n", file)
		return 1
	}
	version := os.Getenv("NEXTERM_RELEASE_VERSION")
	if version == "" {
		fmt.Fprintln(stderr, "NEXTERM_RELEASE_VERSION is required")
		return 1
	}
	limit, err := readLimits()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if info.Size() > limit.maxPackage {
		fmt.Fprintf(stderr, "mobile package exceeds the %d byte limit: %s\n", limit.maxPackage, file)
		return 1
	}
	packageFormat := "apk"
	if id == "mobile-ios" {
		packageFormat = "ipa"
	}
	checker := &checker{file: file, id: id, version: version, limits: limit}
	checker.add("package", "passed", fmt.Sprintf("%s package is present, readable and within the %d byte limit", packageFormat, limit.maxPackage))
	if err := checker.run(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	digest, err := fileDigest(file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	status := "passed"
	for _, item := range checker.assertions {
		if item.Status == "failed" {
			status = "failed"
		}
	}
	root, _ := os.Getwd()
	relative, err := filepath.Rel(root, file)
	if err != nil {
		relative = file
	}
	epoch, _ := strconv.ParseInt(os.Getenv("SOURCE_DATE_EPOCH"), 10, 64)
	commit := os.Getenv("GITHUB_SHA")
	if commit == "" {
		commit = "unknown"
	}
	osName := "android"
	if id == "mobile-ios" {
		osName = "ios"
	}
	output := report{
		SchemaVersion: 2, ID: id, Kind: "mobile",
		Platform:        platform{OS: osName, Arch: "arm64", CGO: "enabled-only-for-wails-platform-layer"},
		Version:         version,
		Commit:          commit,
		SourceDateEpoch: epoch,
		Artifact:        artifact{Path: filepath.ToSlash(relative), SizeBytes: info.Size(), SHA256: digest},
		Format:          format{Format: packageFormat, Architecture: "arm64"},
		Assertions:      checker.assertions,
		Status:          status,
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.WriteFile(file+".artifact.json", append(encoded, '\n'), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stderr, "%s: %d bytes; report=%s.artifact.json\n", id, info.Size(), file)
	for _, item := range checker.assertions {
		if item.Status == "skipped" {
			fmt.Fprintf(stderr, "%s: %s: skipped: %s\n", id, item.ID, item.Detail)
		}
	}
	for _, item := range checker.assertions {
		if item.Status == "failed" {
			fmt.Fprintf(stderr, "%s: %s: %s\n", id, item.ID, item.Detail)
		}
	}
	if status == "failed" {
		return 1
	}
	return 0
}

func environmentCeiling(name string, ceiling int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return ceiling, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 || value > ceiling {
		return 0, fmt.Errorf("%s must be an integer in 1..%d", name, ceiling)
	}
	return value, nil
}

// readLimits validates every budget eagerly, so a bad override fails before
// any tool runs or any report is written.
func readLimits() (limits, error) {
	var result limits
	var err error
	if result.maxPackage, err = environmentCeiling("NEXTERM_MOBILE_EVIDENCE_MAX_PACKAGE_BYTES", 2*gib); err != nil {
		return result, err
	}
	if result.maxTotal, err = environmentCeiling("NEXTERM_MOBILE_EVIDENCE_MAX_TOTAL_UNCOMPRESSED_BYTES", 4*gib); err != nil {
		return result, err
	}
	if result.maxEntry, err = environmentCeiling("NEXTERM_MOBILE_EVIDENCE_MAX_ENTRY_BYTES", 512*mib); err != nil {
		return result, err
	}
	timeout, err := environmentCeiling("NEXTERM_MOBILE_EVIDENCE_TOOL_TIMEOUT_MS", 60000)
	if err != nil {
		return result, err
	}
	result.toolTimeout = time.Duration(timeout) * time.Millisecond
	// A deliberately hanging tool under test must not share a small budget
	// with the real tools around it, so each tool may lower its own budget.
	result.overrides = map[string]time.Duration{}
	for _, tool := range []string{"openssl", "apksigner", "aapt2"} {
		name := "NEXTERM_MOBILE_EVIDENCE_" + strings.ToUpper(tool) + "_TIMEOUT_MS"
		if os.Getenv(name) == "" {
			continue
		}
		value, err := environmentCeiling(name, timeout)
		if err != nil {
			return result, err
		}
		result.overrides[tool] = time.Duration(value) * time.Millisecond
	}
	return result, nil
}

func fileDigest(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type checker struct {
	file, id, version string
	limits            limits
	assertions        []assertion
	manifest          *zipManifest
	handle            *os.File
}

func (c *checker) add(id, status, detail string) {
	c.assertions = append(c.assertions, assertion{ID: id, Status: status, Detail: detail})
}

func (c *checker) run() error {
	handle, err := os.Open(c.file)
	if err != nil {
		return err
	}
	defer handle.Close()
	c.handle = handle
	info, err := handle.Stat()
	if err != nil {
		return err
	}
	var required []string
	if c.id == "mobile-ios" {
		required = []string{iosInfoPlist, iosExecutable, iosProvisioningProfile}
	}
	manifest, err := inspectZip(handle, info.Size(), c.limits.maxEntry, c.limits.maxTotal, required)
	if err != nil {
		c.add("zip-structure", "failed", "Error: ZIP central directory validation failed: "+err.Error())
		return nil
	}
	c.manifest = manifest
	c.add("zip-structure", "passed", fmt.Sprintf("central directory contains %d unique safe entries totaling %d uncompressed bytes", manifest.entryCount, manifest.totalUncompressedBytes))
	if c.id == "mobile-ios" {
		c.checkIOS()
	} else {
		c.checkAndroidSignature()
		c.checkAndroidVersion()
	}
	return nil
}

func (c *checker) entry(name string, limit int64) ([]byte, error) {
	metadata, ok := c.manifest.required[name]
	if !ok {
		return nil, fmt.Errorf("required ZIP entry is missing: %s", name)
	}
	if metadata.Size > limit {
		return nil, fmt.Errorf("ZIP entry %s exceeds the %d byte limit", name, limit)
	}
	return readEntry(c.handle, c.manifest.entries[name], limit)
}

func (c *checker) checkIOS() {
	platformName := "unavailable"
	if metadata, err := c.iosMetadata(); err != nil {
		c.add("ios-version", "failed", "Error: "+err.Error())
	} else {
		platformName = metadata.platformName
		release := parseReleaseVersion(c.version)
		expectedShort := "invalid NEXTERM_RELEASE_VERSION"
		if release != nil {
			expectedShort = release.core
		}
		expectedBuild := os.Getenv("IOS_BUILD_NUMBER")
		buildPassed := validIOSBuildNumber(metadata.bundleVersion)
		expectedBuildText := "a valid numeric build number (IOS_BUILD_NUMBER not supplied)"
		if expectedBuild != "" {
			buildPassed = validIOSBuildNumber(expectedBuild) && metadata.bundleVersion == expectedBuild
			expectedBuildText = expectedBuild
		}
		passed := release != nil && buildPassed && metadata.shortVersion == expectedShort
		c.add("ios-version", passFail(passed), fmt.Sprintf("CFBundleShortVersionString=%s, CFBundleVersion=%s; expected CFBundleShortVersionString=%s, CFBundleVersion=%s",
			metadata.shortVersion, metadata.bundleVersion, expectedShort, expectedBuildText))
	}
	if data, err := c.entry(iosExecutable, maxExecutableBytes); err != nil {
		c.add("ios-platform", "failed", "Error: "+err.Error())
	} else if platforms, err := machOBuildPlatforms(data); err != nil {
		c.add("ios-platform", "failed", "Error: "+err.Error())
	} else {
		passed := platformName == "iphoneos" && len(platforms) > 0
		names := make([]string, len(platforms))
		for index, value := range platforms {
			names[index] = strconv.FormatUint(value, 10)
			if value != iosDevicePlatform {
				passed = false
			}
		}
		c.add("ios-platform", passFail(passed), fmt.Sprintf("DTPlatformName=%s; Mach-O LC_BUILD_VERSION platforms=%s; expected DTPlatformName=iphoneos and every slice platform=2 (iOS device)", platformName, strings.Join(names, ",")))
	}
	if profile, err := c.provisioningProfile(); err != nil {
		c.add("ios-provisioning-profile", "failed", "Error: "+err.Error())
	} else {
		team := ""
		if len(profile.teamIdentifiers) > 0 {
			team = profile.teamIdentifiers[0]
		}
		expected := "a team-qualified work.nexterm.app"
		if team != "" {
			expected = team + ".work.nexterm.app"
		}
		passed := team != "" && profile.applicationIdentifier == expected && profile.getTaskAllow == "false" && !profile.hasProvisionedDevices && !profile.hasProvisionsAllDevices
		c.add("ios-provisioning-profile", passFail(passed), fmt.Sprintf("application-identifier=%s; expected %s; get-task-allow=%s; ProvisionedDevices present=%t; ProvisionsAllDevices present=%t; CMS signature verified with the embedded signer (certificate trust not checked)",
			profile.applicationIdentifier, expected, profile.getTaskAllow, profile.hasProvisionedDevices, profile.hasProvisionsAllDevices))
	}
	if metadata, ok := c.manifest.required[iosExecutable]; !ok {
		c.add("ios-executable-mode", "failed", "Error: required ZIP entry is missing: "+iosExecutable)
	} else {
		c.add("ios-executable-mode", passFail(metadata.Mode == 0o755), fmt.Sprintf("%s mode=%04o; expected 0755", iosExecutable, metadata.Mode))
	}
}

func passFail(passed bool) string {
	if passed {
		return "passed"
	}
	return "failed"
}

type iosMetadata struct{ shortVersion, bundleVersion, platformName string }

func (c *checker) iosMetadata() (iosMetadata, error) {
	data, err := c.entry(iosInfoPlist, maxPlistOrProfileBytes)
	if err != nil {
		return iosMetadata{}, err
	}
	parsed, err := parsePlist(data)
	if err != nil {
		return iosMetadata{}, fmt.Errorf("cannot parse %s: %v", iosInfoPlist, err)
	}
	dict, ok := parsed.(map[string]any)
	if !ok {
		return iosMetadata{}, fmt.Errorf("cannot parse %s: plist root is not a dictionary", iosInfoPlist)
	}
	values := map[string]string{}
	for _, key := range []string{"CFBundleShortVersionString", "CFBundleVersion", "DTPlatformName"} {
		value, ok := dict[key].(string)
		if !ok {
			return iosMetadata{}, fmt.Errorf("cannot parse %s: %s is not a string", iosInfoPlist, key)
		}
		values[key] = value
	}
	return iosMetadata{values["CFBundleShortVersionString"], values["CFBundleVersion"], values["DTPlatformName"]}, nil
}

type profile struct {
	teamIdentifiers                                []string
	applicationIdentifier, getTaskAllow            string
	hasProvisionedDevices, hasProvisionsAllDevices bool
}

func (c *checker) provisioningProfile() (profile, error) {
	data, err := c.entry(iosProvisioningProfile, maxPlistOrProfileBytes)
	if err != nil {
		return profile{}, err
	}
	decoded, err := c.runTool("openssl", []string{"cms", "-verify", "-binary", "-inform", "DER", "-noverify"}, data)
	if err != nil {
		return profile{}, fmt.Errorf("cannot verify %s CMS signature: %v", iosProvisioningProfile, err)
	}
	label := iosProvisioningProfile + " CMS payload"
	parsed, err := parsePlist(decoded)
	if err != nil {
		return profile{}, fmt.Errorf("cannot parse %s: %v", label, err)
	}
	dict, ok := parsed.(map[string]any)
	if !ok {
		return profile{}, fmt.Errorf("cannot parse %s: provisioning profile is not a dictionary", label)
	}
	teams, ok := dict["TeamIdentifier"].([]any)
	if !ok {
		return profile{}, fmt.Errorf("cannot parse %s: TeamIdentifier is not a string array", label)
	}
	result := profile{}
	for _, team := range teams {
		name, ok := team.(string)
		if !ok {
			return profile{}, fmt.Errorf("cannot parse %s: TeamIdentifier is not a string array", label)
		}
		result.teamIdentifiers = append(result.teamIdentifiers, name)
	}
	entitlements, ok := dict["Entitlements"].(map[string]any)
	if !ok {
		return profile{}, fmt.Errorf("cannot parse %s: Entitlements is not a dictionary", label)
	}
	result.applicationIdentifier = describe(entitlements["application-identifier"])
	result.getTaskAllow = describe(entitlements["get-task-allow"])
	_, result.hasProvisionedDevices = dict["ProvisionedDevices"]
	_, result.hasProvisionsAllDevices = dict["ProvisionsAllDevices"]
	return result, nil
}

// describe renders a plist value the way the report states it; a missing value
// is "undefined" so it can never equal an expected string or boolean.
func describe(value any) string {
	switch typed := value.(type) {
	case nil:
		return "undefined"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

type toolError struct{ message string }

func (err toolError) Error() string { return err.message }

func (c *checker) runTool(command string, args []string, input []byte) ([]byte, error) {
	stdout, _, err := c.runToolOutput(command, args, input)
	return stdout, err
}

func (c *checker) runToolOutput(command string, args []string, input []byte) ([]byte, string, error) {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(command), filepath.Ext(command)))
	timeout := c.limits.toolTimeout
	if override, ok := c.limits.overrides[base]; ok {
		timeout = override
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.WaitDelay = time.Second
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxToolOutputBytes, maxToolOutputBytes
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, "", toolError{fmt.Sprintf("%s timed out after %dms", base, timeout.Milliseconds())}
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, "", toolError{fmt.Sprintf("%s output exceeds the %d byte limit", base, maxToolOutputBytes)}
	}
	if err != nil {
		output := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		if output == "" {
			output = err.Error()
		}
		return nil, "", toolError{output}
	}
	return stdout.Bytes(), stderr.String(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int64
	exceeded bool
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	if int64(buffer.Len()+len(data)) > buffer.limit {
		buffer.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	return buffer.Buffer.Write(data)
}

func executableFile(candidate string) bool {
	info, err := os.Stat(candidate)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func androidTool(name string) string {
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory != "" && executableFile(filepath.Join(directory, name)) {
			return filepath.Join(directory, name)
		}
	}
	seen := map[string]bool{}
	for _, root := range []string{os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT")} {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		entries, err := os.ReadDir(filepath.Join(root, "build-tools"))
		if err != nil {
			continue
		}
		versions := make([]string, 0, len(entries))
		for _, entry := range entries {
			versions = append(versions, entry.Name())
		}
		sort.Slice(versions, func(i, j int) bool { return naturalLess(versions[j], versions[i]) })
		for _, installed := range versions {
			candidate := filepath.Join(root, "build-tools", installed, name)
			if executableFile(candidate) {
				return candidate
			}
		}
	}
	return ""
}

var digitRuns = regexp.MustCompile(`\d+|\D+`)

// naturalLess orders build-tools directories so that 35.0.0 sorts after 9.0.0.
func naturalLess(left, right string) bool {
	a, b := digitRuns.FindAllString(left, -1), digitRuns.FindAllString(right, -1)
	for index := 0; index < len(a) && index < len(b); index++ {
		x, xErr := strconv.ParseUint(a[index], 10, 64)
		y, yErr := strconv.ParseUint(b[index], 10, 64)
		if xErr == nil && yErr == nil {
			if x != y {
				return x < y
			}
			continue
		}
		if a[index] != b[index] {
			return a[index] < b[index]
		}
	}
	return len(a) < len(b)
}

var certificateDigest = regexp.MustCompile(`(?i)certificate\s+SHA-256\s+digest:\s*([0-9a-f]{64})`)

func (c *checker) checkAndroidSignature() {
	tool := androidTool("apksigner")
	if tool == "" {
		c.add("android-signature", "skipped", "apksigner was not found on PATH or in Android SDK build-tools; APK signature and pinned certificate were not verified")
		return
	}
	pinned := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(os.Getenv("NEXTERM_ANDROID_CERT_SHA256"), ":", "")))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(pinned) {
		c.add("android-signature", "failed", "NEXTERM_ANDROID_CERT_SHA256 must contain one SHA-256 hex digest")
		return
	}
	output, err := c.runToolCombined(tool, []string{"verify", "--print-certs", c.file})
	if err != nil {
		c.add("android-signature", "failed", "apksigner verify failed: "+err.Error())
		return
	}
	var digests []string
	passed := false
	for _, match := range certificateDigest.FindAllStringSubmatch(output, -1) {
		digest := strings.ToLower(match[1])
		digests = append(digests, digest)
		passed = passed || digest == pinned
	}
	if passed {
		c.add("android-signature", "passed", "apksigner verified the APK and signer certificate SHA-256 "+pinned)
		return
	}
	found := strings.Join(digests, ", ")
	if found == "" {
		found = "no certificate digest found"
	}
	c.add("android-signature", "failed", fmt.Sprintf("pinned signer certificate SHA-256 %s did not match apksigner output: %s", pinned, found))
}

// runToolCombined returns stdout followed by stderr, as apksigner prints
// certificate digests on either stream depending on the version.
func (c *checker) runToolCombined(command string, args []string) (string, error) {
	stdout, stderr, err := c.runToolOutput(command, args, nil)
	return string(stdout) + "\n" + stderr, err
}

var (
	versionNamePattern = regexp.MustCompile(`\bversionName='([^']*)'`)
	versionCodePattern = regexp.MustCompile(`\bversionCode='(\d+)'`)
)

func (c *checker) checkAndroidVersion() {
	tool := androidTool("aapt2")
	if tool == "" {
		c.add("android-version", "skipped", "aapt2 was not found on PATH or in Android SDK build-tools; APK versionName and versionCode were not verified")
		return
	}
	expectedCode, err := expectedAndroidVersionCode(c.version)
	if err != nil {
		c.add("android-version", "failed", "Error: "+err.Error())
		return
	}
	output, err := c.runTool(tool, []string{"dump", "badging", c.file}, nil)
	if err != nil {
		c.add("android-version", "failed", "aapt2 dump badging failed: "+err.Error())
		return
	}
	var packageLine string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "package: ") {
			packageLine = line
			break
		}
	}
	actualName, actualCode := "missing", "missing"
	if match := versionNamePattern.FindStringSubmatch(packageLine); match != nil {
		actualName = match[1]
	}
	if match := versionCodePattern.FindStringSubmatch(packageLine); match != nil {
		actualCode = match[1]
	}
	passed := actualName == c.version && actualCode == strconv.FormatInt(expectedCode, 10)
	c.add("android-version", passFail(passed), fmt.Sprintf("versionName=%s, versionCode=%s; expected versionName=%s, versionCode=%d", actualName, actualCode, c.version, expectedCode))
}

func expectedAndroidVersionCode(value string) (int64, error) {
	parsed := parseReleaseVersion(value)
	if parsed == nil {
		return 0, fmt.Errorf("NEXTERM_RELEASE_VERSION is not a non-placeholder semantic version: %s", value)
	}
	major, _ := strconv.ParseInt(parsed.major, 10, 64)
	minor, _ := strconv.ParseInt(parsed.minor, 10, 64)
	patch, _ := strconv.ParseInt(parsed.patch, 10, 64)
	if minor > 999 || patch > 999 {
		return 0, errors.New("Android versionCode requires minor and patch versions no greater than 999")
	}
	if major > androidVersionCodeLimit/1000000 {
		return 0, fmt.Errorf("Android versionCode for major version %d is outside the supported range 1..%d", major, androidVersionCodeLimit)
	}
	code := major*1000000 + minor*1000 + patch
	if code < 1 || code > androidVersionCodeLimit {
		return 0, fmt.Errorf("Android versionCode %d is outside the supported range 1..%d", code, androidVersionCodeLimit)
	}
	return code, nil
}

var iosBuildNumber = regexp.MustCompile(`^\d+(?:\.\d+){0,2}$`)

func validIOSBuildNumber(value string) bool {
	if !iosBuildNumber.MatchString(value) {
		return false
	}
	for index, part := range strings.Split(value, ".") {
		number, err := strconv.ParseUint(part, 10, 53)
		if err != nil || (index == 0 && number == 0) {
			return false
		}
	}
	return true
}

var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

type releaseVersion struct{ core, major, minor, patch string }

// parseReleaseVersion mirrors scripts/lib/semver.mjs: a SemVer 2.0 version
// other than the 0.0.0 placeholder.
func parseReleaseVersion(value string) *releaseVersion {
	match := semverPattern.FindStringSubmatch(value)
	if match == nil || value == "0.0.0" {
		return nil
	}
	return &releaseVersion{core: match[1] + "." + match[2] + "." + match[3], major: match[1], minor: match[2], patch: match[3]}
}
