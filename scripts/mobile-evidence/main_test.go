package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"
)

var (
	signerOnce sync.Once
	signerKey  string
	signerCert string
	signerErr  error
)

func requireOpenSSL(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is required for provisioning profile fixtures")
	}
	signerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nexterm-profile-signer-")
		if err != nil {
			signerErr = err
			return
		}
		signerKey, signerCert = filepath.Join(dir, "key.pem"), filepath.Join(dir, "cert.pem")
		signerErr = exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", signerKey, "-out", signerCert, "-days", "1", "-subj", "/CN=NexTerm mobile evidence fixture").Run()
	})
	if signerErr != nil {
		t.Fatal(signerErr)
	}
}

func infoPlist(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "build", "ios", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func setPlistString(source, key, value string) string {
	pattern := regexp.MustCompile(`(<key>` + key + `</key>\s*<string>)[^<]*`)
	return pattern.ReplaceAllString(source, "${1}"+value)
}

func plistWithVersions(t *testing.T, shortVersion, bundleVersion string) string {
	plist := setPlistString(infoPlist(t), "CFBundleShortVersionString", shortVersion)
	plist = setPlistString(plist, "CFBundleVersion", bundleVersion)
	return setPlistString(plist, "CFBundleGetInfoString", "1.1.0")
}

func thinMachO(platform uint32) []byte {
	data := make([]byte, 56)
	binary.LittleEndian.PutUint32(data[0:], 0xfeedfacf)
	binary.LittleEndian.PutUint32(data[4:], 0x0100000c)
	binary.LittleEndian.PutUint32(data[12:], 6)
	binary.LittleEndian.PutUint32(data[16:], 1)
	binary.LittleEndian.PutUint32(data[20:], 24)
	binary.LittleEndian.PutUint32(data[32:], 0x32)
	binary.LittleEndian.PutUint32(data[36:], 24)
	binary.LittleEndian.PutUint32(data[40:], platform)
	binary.LittleEndian.PutUint32(data[44:], 0x000f0000)
	binary.LittleEndian.PutUint32(data[48:], 0x000f0000)
	return data
}

func makeMachO(platforms ...uint32) []byte {
	if len(platforms) == 1 {
		return thinMachO(platforms[0])
	}
	offset := 8 + len(platforms)*20
	slices := make([][]byte, len(platforms))
	offsets := make([]int, len(platforms))
	for index, value := range platforms {
		slices[index] = thinMachO(value)
		offset = (offset + 3) &^ 3
		offsets[index] = offset
		offset += len(slices[index])
	}
	data := make([]byte, offset)
	binary.BigEndian.PutUint32(data[0:], 0xcafebabe)
	binary.BigEndian.PutUint32(data[4:], uint32(len(platforms)))
	for index, slice := range slices {
		architecture := 8 + index*20
		binary.BigEndian.PutUint32(data[architecture:], 0x0100000c)
		binary.BigEndian.PutUint32(data[architecture+8:], uint32(offsets[index]))
		binary.BigEndian.PutUint32(data[architecture+12:], uint32(len(slice)))
		binary.BigEndian.PutUint32(data[architecture+16:], 2)
		copy(data[offsets[index]:], slice)
	}
	return data
}

type profileOptions struct {
	applicationIdentifier                                  string
	getTaskAllow, provisionedDevices, provisionsAllDevices bool
}

func makeProfile(t *testing.T, options profileOptions) []byte {
	t.Helper()
	requireOpenSSL(t)
	if options.applicationIdentifier == "" {
		options.applicationIdentifier = "TEAMID.work.nexterm.app"
	}
	markers := ""
	if options.provisionedDevices {
		markers += "<key>ProvisionedDevices</key><array><string>0123456789abcdef</string></array>"
	}
	if options.provisionsAllDevices {
		markers += "<key>ProvisionsAllDevices</key><true/>"
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>AppIDName</key><string>NexTerm evidence fixture</string>
<key>Name</key><string>NexTerm App Store evidence fixture</string>
<key>UUID</key><string>01234567-89ab-cdef-0123-456789abcdef</string>
<key>TeamIdentifier</key><array><string>TEAMID</string></array>
<key>ExpirationDate</key><date>2099-01-01T00:00:00Z</date>
<key>Entitlements</key><dict>
<key>application-identifier</key><string>%s</string>
<key>get-task-allow</key><%t/>
</dict>%s
</dict></plist>`, options.applicationIdentifier, options.getTaskAllow, markers)
	cmd := exec.Command("openssl", "cms", "-sign", "-binary", "-nodetach", "-nosmimecap", "-outform", "DER", "-signer", signerCert, "-inkey", signerKey)
	cmd.Stdin = strings.NewReader(plist)
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return output
}

type zipFixture struct {
	name string
	data []byte
	mode os.FileMode
	dir  bool
}

func writeZip(t *testing.T, path string, method uint16, entries []zipFixture) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: method}
		header.ModifiedDate, header.ModifiedTime = dosDate(1980), 0
		if entry.dir {
			header.Method = zip.Store
			header.SetMode(os.ModeDir | 0o755)
		} else if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		writeRaw(t, writer, header, entry.data)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeRaw stores precomputed sizes and CRC so that no data descriptor is
// written, matching what zip, zipalign and Xcode produce.
func writeRaw(t *testing.T, writer *zip.Writer, header *zip.FileHeader, data []byte) {
	t.Helper()
	payload := data
	if header.Method == zip.Deflate {
		var compressed bytes.Buffer
		deflater, _ := flate.NewWriter(&compressed, flate.DefaultCompression)
		deflater.Write(data)
		deflater.Close()
		payload = compressed.Bytes()
	}
	header.CRC32 = crc32.ChecksumIEEE(data)
	header.UncompressedSize64 = uint64(len(data))
	header.CompressedSize64 = uint64(len(payload))
	out, err := writer.CreateRaw(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write(payload); err != nil {
		t.Fatal(err)
	}
}

func dosDate(year int) uint16 { return uint16(1 + 1<<5 + (year-1980)<<9) }

type ipaOptions struct {
	executable     []byte
	executableName string
	executableMode os.FileMode
	profile        []byte
	noProfile      bool
}

func makePlistIPA(t *testing.T, plist []byte, options ipaOptions) string {
	t.Helper()
	if options.executable == nil {
		options.executable = makeMachO(2)
	}
	if options.executableName == "" {
		options.executableName = "nexterm"
	}
	if options.executableMode == 0 {
		options.executableMode = 0o755
	}
	entries := []zipFixture{
		{name: "Payload/", dir: true},
		{name: "Payload/nexterm.app/", dir: true},
		{name: "Payload/nexterm.app/Info.plist", data: plist, mode: 0o644},
		{name: "Payload/nexterm.app/" + options.executableName, data: options.executable, mode: options.executableMode},
	}
	base := "Payload/nexterm.app/Frameworks/onnxruntime.framework/"
	entries = append(entries, zipFixture{name: base + "onnxruntime", data: thinMachO(2), mode: 0o755}, zipFixture{name: base + "Info.plist", data: []byte("<plist><dict><key>CFBundleExecutable</key><string>onnxruntime</string></dict></plist>"), mode: 0o644})
	entries = append(entries, privacyLicenseFixtures("Payload/nexterm.app/ai-runtime/", "ios", "arm64")...)
	if !options.noProfile {
		profile := options.profile
		if profile == nil {
			profile = makeProfile(t, profileOptions{})
		}
		entries = append(entries, zipFixture{name: "Payload/nexterm.app/embedded.mobileprovision", data: profile, mode: 0o644})
	}
	path := filepath.Join(t.TempDir(), "test.ipa")
	writeZip(t, path, zip.Deflate, entries)
	return path
}

func makeIPA(t *testing.T, shortVersion, bundleVersion string, options ipaOptions) string {
	return makePlistIPA(t, []byte(plistWithVersions(t, shortVersion, bundleVersion)), options)
}

func makeAndroidAPK(t *testing.T, name string, data []byte) string {
	t.Helper()
	if name == "" {
		name = "AndroidManifest.xml"
	}
	if data == nil {
		data = []byte("fixture")
	}
	path := filepath.Join(t.TempDir(), "test.apk")
	entries := privacyLicenseFixtures("assets/ai-runtime/arm64-v8a/", "android", "arm64")
	entries = append(entries, zipFixture{name: "lib/arm64-v8a/libwails.so", data: []byte("fixture")}, zipFixture{name: "lib/arm64-v8a/libonnxruntime.so", data: privacyELF(183)}, zipFixture{name: name, data: data})
	writeZip(t, path, zip.Store, entries)
	return path
}

// appendZipEntry rewrites the archive with its raw entries plus one more,
// which may duplicate an existing name.
func appendZipEntry(t *testing.T, path, name string, data []byte, mode os.FileMode) {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, file := range reader.File {
		if err := writer.Copy(file); err != nil {
			t.Fatal(err)
		}
	}
	reader.Close()
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.ModifiedDate = dosDate(1980)
	header.CreatorVersion = 3 << 8
	header.ExternalAttrs = uint32(mode) << 16
	writeRaw(t, writer, header, data)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

type locations struct {
	data                       []byte
	localHeader, centralHeader int
}

func zipEntryLocations(t *testing.T, path, entry string) locations {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	name := []byte(entry)
	local := bytes.Index(data, name)
	central := bytes.Index(data[local+len(name):], name) + local + len(name)
	if local < 30 || central < 46 || binary.LittleEndian.Uint32(data[local-30:]) != localHeaderSignature || binary.LittleEndian.Uint32(data[central-46:]) != centralHeaderSignature {
		t.Fatalf("cannot locate headers for %s", entry)
	}
	return locations{data: data, localHeader: local - 30, centralHeader: central - 46}
}

func localDataOffset(data []byte, localHeader int) int {
	return localHeader + 30 + int(binary.LittleEndian.Uint16(data[localHeader+26:])) + int(binary.LittleEndian.Uint16(data[localHeader+28:]))
}

func centralStart(t *testing.T, data []byte) int {
	t.Helper()
	position := bytes.LastIndex(data, []byte{0x50, 0x4b, 0x05, 0x06})
	if position < 0 {
		t.Fatal("no end record")
	}
	return int(binary.LittleEndian.Uint32(data[position+16:]))
}

// patchZipEntryName replaces both the local and central filename.
func patchZipEntryName(t *testing.T, path, original, replacement string) {
	t.Helper()
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), original) != 2 || len(original) != len(replacement) {
		t.Fatalf("cannot patch %q", original)
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(data, []byte(original), []byte(replacement)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// patchLocalZipEntryName replaces only the local header filename.
func patchLocalZipEntryName(t *testing.T, path, original, replacement string) {
	t.Helper()
	found := zipEntryLocations(t, path, original)
	copy(found.data[found.localHeader+30:], replacement)
	if err := os.WriteFile(path, found.data, 0o644); err != nil {
		t.Fatal(err)
	}
}

const androidCert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type androidTools struct {
	certificate, versionName, versionCode, signerLabel string
	hang                                               bool
}

func makeAndroidTools(t *testing.T, options androidTools) string {
	t.Helper()
	if options.certificate == "" {
		options.certificate = androidCert
	}
	if options.versionName == "" {
		options.versionName = "1.1.0"
	}
	if options.versionCode == "" {
		options.versionCode = "1001000"
	}
	if options.signerLabel == "" {
		options.signerLabel = "Signer #1"
	}
	root := t.TempDir()
	bin := filepath.Join(root, "build-tools", "35.0.0")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	signer := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s certificate SHA-256 digest: %s'\n", options.signerLabel, options.certificate)
	aapt := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"package: name='work.nexterm.app' versionCode='%s' versionName='%s'\"\n", options.versionCode, options.versionName)
	if options.hang {
		signer = hangingScript(t)
		aapt = signer
	}
	os.WriteFile(filepath.Join(bin, "apksigner"), []byte(signer), 0o755)
	os.WriteFile(filepath.Join(bin, "aapt2"), []byte(aapt), 0o755)
	return root
}

// hangingScript resolves sleep up front because Android tests run with an empty PATH.
func hangingScript(t *testing.T) string {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is required for hanging tool fixtures")
	}
	return "#!/bin/sh\nexec " + sleep + " 10\n"
}

func hangingTool(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(hangingScript(t)), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

type result struct {
	status int
	stderr string
	report *report
}

func (r result) has(id, status, detail string) bool {
	if r.report == nil {
		return false
	}
	for _, item := range r.report.Assertions {
		if item.ID == id && item.Status == status && strings.Contains(item.Detail, detail) {
			return true
		}
	}
	return false
}

func (r result) hasPrefix(prefix string) bool {
	if r.report == nil {
		return false
	}
	for _, item := range r.report.Assertions {
		if strings.HasPrefix(item.ID, prefix) {
			return true
		}
	}
	return false
}

var evidenceVariables = []string{
	"NEXTERM_RELEASE_VERSION", "IOS_BUILD_NUMBER", "ANDROID_HOME", "ANDROID_SDK_ROOT", "NEXTERM_ANDROID_CERT_SHA256",
	"NEXTERM_MOBILE_EVIDENCE_MAX_PACKAGE_BYTES", "NEXTERM_MOBILE_EVIDENCE_MAX_TOTAL_UNCOMPRESSED_BYTES",
	"NEXTERM_MOBILE_EVIDENCE_MAX_ENTRY_BYTES", "NEXTERM_MOBILE_EVIDENCE_TOOL_TIMEOUT_MS",
	"NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS", "NEXTERM_MOBILE_EVIDENCE_APKSIGNER_TIMEOUT_MS", "NEXTERM_MOBILE_EVIDENCE_AAPT2_TIMEOUT_MS",
}

func evidence(t *testing.T, path, id string, environment map[string]string) result {
	t.Helper()
	for _, name := range evidenceVariables {
		t.Setenv(name, "")
	}
	t.Setenv("NEXTERM_RELEASE_VERSION", "1.1.0")
	for name, value := range environment {
		t.Setenv(name, value)
	}
	os.Remove(path + ".artifact.json")
	var stderr bytes.Buffer
	status := run([]string{path, id}, &stderr)
	out := result{status: status, stderr: stderr.String()}
	if raw, err := os.ReadFile(path + ".artifact.json"); err == nil {
		out.report = &report{}
		if err := json.Unmarshal(raw, out.report); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func iosEvidence(t *testing.T, path string) result { return evidence(t, path, "mobile-ios", nil) }

func androidEnvironment(t *testing.T, sdk, certificate string) map[string]string {
	return map[string]string{"PATH": t.TempDir(), "ANDROID_HOME": sdk, "ANDROID_SDK_ROOT": "", "NEXTERM_ANDROID_CERT_SHA256": certificate}
}

func androidEvidence(t *testing.T, path string, environment map[string]string) result {
	return evidence(t, path, "mobile-android", environment)
}

func expectZipFailure(t *testing.T, got result, detail, prefix string) {
	t.Helper()
	if got.status != 1 || !got.has("zip-structure", "failed", detail) || got.hasPrefix(prefix) {
		t.Fatalf("status=%d stderr=%s report=%+v; want zip-structure failure %q without %s checks", got.status, got.stderr, got.report, detail, prefix)
	}
}

func expectStatus(t *testing.T, got result, status int) {
	t.Helper()
	if got.status != status {
		t.Fatalf("status = %d, want %d; stderr=%s", got.status, status, got.stderr)
	}
}

func TestPackageAndZipLimits(t *testing.T) {
	ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
	got := evidence(t, ipa, "mobile-ios", map[string]string{"NEXTERM_MOBILE_EVIDENCE_MAX_PACKAGE_BYTES": "16"})
	if got.status != 1 || !strings.Contains(got.stderr, "mobile package exceeds the 16 byte limit") || got.report != nil {
		t.Fatalf("package limit: %+v", got)
	}
	got = evidence(t, ipa, "mobile-ios", map[string]string{"NEXTERM_MOBILE_EVIDENCE_MAX_TOTAL_UNCOMPRESSED_BYTES": "16"})
	expectZipFailure(t, got, "total uncompressed size exceeds the 16 byte limit", "ios-")
	got = evidence(t, ipa, "mobile-ios", map[string]string{"NEXTERM_MOBILE_EVIDENCE_MAX_ENTRY_BYTES": "16"})
	expectZipFailure(t, got, "ZIP entry exceeds the 16 byte limit", "ios-")
}

func TestRejectsOversizedInfoPlist(t *testing.T) {
	oversized := append([]byte(plistWithVersions(t, "1.1.0", "1.1.0")), make([]byte, 16*1024*1024)...)
	got := iosEvidence(t, makePlistIPA(t, oversized, ipaOptions{}))
	expectStatus(t, got, 1)
	if !got.has("zip-structure", "passed", "") || !got.has("ios-version", "failed", "16777216 byte limit") || !got.has("ios-provisioning-profile", "passed", "") {
		t.Fatalf("report = %+v", got.report)
	}
}

func TestToolTimeouts(t *testing.T) {
	ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
	// Only openssl gets the small budget.
	got := evidence(t, ipa, "mobile-ios", map[string]string{
		"PATH": hangingTool(t, "openssl") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS": "250",
	})
	expectStatus(t, got, 1)
	if !got.has("ios-platform", "passed", "") || !got.has("ios-provisioning-profile", "failed", "timed out") {
		t.Fatalf("report = %+v", got.report)
	}

	apk := makeAndroidAPK(t, "", nil)
	environment := androidEnvironment(t, makeAndroidTools(t, androidTools{hang: true}), androidCert)
	environment["NEXTERM_MOBILE_EVIDENCE_APKSIGNER_TIMEOUT_MS"] = "250"
	environment["NEXTERM_MOBILE_EVIDENCE_AAPT2_TIMEOUT_MS"] = "250"
	got = androidEvidence(t, apk, environment)
	expectStatus(t, got, 1)
	for _, id := range []string{"android-signature", "android-version"} {
		if !got.has(id, "failed", "timed out") {
			t.Fatalf("%s did not time out: %+v", id, got.report)
		}
	}
}

func TestToolTimeoutOverridesAreValidated(t *testing.T) {
	ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
	for _, environment := range []map[string]string{
		{"NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS": "0"},
		{"NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS": "not-a-number"},
		{"NEXTERM_MOBILE_EVIDENCE_TOOL_TIMEOUT_MS": "1000", "NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS": "2000"},
	} {
		got := evidence(t, ipa, "mobile-ios", environment)
		if got.status != 1 || !regexp.MustCompile(`NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS must be an integer in 1\.\.\d+`).MatchString(got.stderr) || got.report != nil {
			t.Fatalf("%v: %+v", environment, got)
		}
	}
}

func TestRejectsDuplicateEntries(t *testing.T) {
	ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{executable: makeMachO(2)})
	appendZipEntry(t, ipa, "Payload/nexterm.app/nexterm", makeMachO(7), 0o100755)
	expectZipFailure(t, iosEvidence(t, ipa), "duplicate ZIP entry", "ios-")

	for _, name := range []string{"Payload/nexterm.app/Info.plist", "Payload/nexterm.app/embedded.mobileprovision"} {
		ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
		appendZipEntry(t, ipa, name, []byte("duplicate"), 0o100644)
		expectZipFailure(t, iosEvidence(t, ipa), "duplicate ZIP entry", "ios-")
	}

	apk := makeAndroidAPK(t, "", nil)
	appendZipEntry(t, apk, "AndroidManifest.xml", []byte("duplicate"), 0o100644)
	expectZipFailure(t, androidEvidence(t, apk, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), "duplicate ZIP entry", "android-")
}

func TestRejectsUnsafeAndNonRegularEntries(t *testing.T) {
	ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
	appendZipEntry(t, ipa, "Payload/../escape", []byte("unsafe"), 0o100644)
	expectZipFailure(t, iosEvidence(t, ipa), "unsafe ZIP entry path", "ios-")

	ipa = makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
	appendZipEntry(t, ipa, "Payload/nexterm.app/linked", []byte("nexterm"), 0o120777)
	expectZipFailure(t, iosEvidence(t, ipa), "unsupported ZIP entry type", "ios-")
}

func TestAcceptsMatchingPackages(t *testing.T) {
	ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
	got := iosEvidence(t, ipa)
	expectStatus(t, got, 0)
	if got.report.Status != "passed" || !got.has("zip-structure", "passed", "") || !got.has("ios-version", "passed", "") || !got.has("ios-executable-mode", "passed", "mode=0755") {
		t.Fatalf("report = %+v", got.report)
	}
	if got.report.Artifact.SizeBytes == 0 || len(got.report.Artifact.SHA256) != 64 || got.report.Format.Format != "ipa" || got.report.Platform.OS != "ios" {
		t.Fatalf("report metadata = %+v", got.report)
	}

	apk := makeAndroidAPK(t, "", nil)
	expectStatus(t, androidEvidence(t, apk, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), 0)

	empty := makeAndroidAPK(t, "empty.bin", []byte{})
	found := zipEntryLocations(t, empty, "empty.bin")
	if localDataOffset(found.data, found.localHeader) != centralStart(t, found.data) {
		t.Fatal("fixture data does not end at the central directory")
	}
	expectStatus(t, androidEvidence(t, empty, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), 0)
}

func TestRejectsShiftedAndOverlappingData(t *testing.T) {
	for _, test := range []struct{ id, entry string }{{"mobile-android", "AndroidManifest.xml"}, {"mobile-ios", "Payload/nexterm.app/nexterm"}} {
		path := makeAndroidAPK(t, "", nil)
		if test.id == "mobile-ios" {
			path = makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
		}
		found := zipEntryLocations(t, path, test.entry)
		extra := centralStart(t, found.data) - localDataOffset(found.data, found.localHeader)
		if extra <= 0 || extra > 0xffff {
			t.Fatalf("extra = %d", extra)
		}
		binary.LittleEndian.PutUint16(found.data[found.localHeader+28:], uint16(extra))
		os.WriteFile(path, found.data, 0o644)
		got := evidence(t, path, test.id, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert))
		if test.id == "mobile-ios" {
			got = iosEvidence(t, path)
		}
		prefix := "android-"
		if test.id == "mobile-ios" {
			prefix = "ios-"
		}
		expectZipFailure(t, got, "compressed data overlaps", prefix)
	}

	apk := makeAndroidAPK(t, "", nil)
	appendZipEntry(t, apk, "second.bin", []byte("second"), 0o100644)
	first := zipEntryLocations(t, apk, "AndroidManifest.xml")
	second := zipEntryLocations(t, apk, "second.bin")
	size := second.localHeader - localDataOffset(first.data, first.localHeader) + 1
	binary.LittleEndian.PutUint32(first.data[first.localHeader+18:], uint32(size))
	binary.LittleEndian.PutUint32(first.data[first.centralHeader+20:], uint32(size))
	os.WriteFile(apk, first.data, 0o644)
	expectZipFailure(t, androidEvidence(t, apk, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), "compressed data overlaps", "android-")
}

func TestRejectsUnboundableLocalHeaders(t *testing.T) {
	apk := makeAndroidAPK(t, "", nil)
	found := zipEntryLocations(t, apk, "AndroidManifest.xml")
	binary.LittleEndian.PutUint16(found.data[found.localHeader+6:], binary.LittleEndian.Uint16(found.data[found.localHeader+6:])|0x8)
	binary.LittleEndian.PutUint16(found.data[found.centralHeader+8:], binary.LittleEndian.Uint16(found.data[found.centralHeader+8:])|0x8)
	os.WriteFile(apk, found.data, 0o644)
	expectZipFailure(t, androidEvidence(t, apk, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), "data descriptor", "android-")

	apk = makeAndroidAPK(t, "", nil)
	found = zipEntryLocations(t, apk, "AndroidManifest.xml")
	binary.LittleEndian.PutUint32(found.data[found.localHeader+18:], 0xffffffff)
	os.WriteFile(apk, found.data, 0o644)
	expectZipFailure(t, androidEvidence(t, apk, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), "ZIP64 local size fields", "android-")
}

func TestRejectsLocalCentralNameMismatches(t *testing.T) {
	for _, nulCount := range []int{1, 4} {
		central := "AndroidManifest.xml"
		apk := makeAndroidAPK(t, "", nil)
		patchLocalZipEntryName(t, apk, central, central[:len(central)-nulCount]+strings.Repeat("\x00", nulCount))
		expectZipFailure(t, androidEvidence(t, apk, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), "local filename", "android-")

		central = "Payload/nexterm.app/nexterm"
		ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
		patchLocalZipEntryName(t, ipa, central, central[:len(central)-nulCount]+strings.Repeat("\x00", nulCount))
		expectZipFailure(t, iosEvidence(t, ipa), "local filename", "ios-")
	}

	apk := makeAndroidAPK(t, "", nil)
	patchLocalZipEntryName(t, apk, "AndroidManifest.xml", "AndroidManifest.xmX")
	expectZipFailure(t, androidEvidence(t, apk, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), "local filename differs", "android-")

	ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
	patchLocalZipEntryName(t, ipa, "Payload/nexterm.app/nexterm", "Payload/nexterm.app/nexterX")
	expectZipFailure(t, iosEvidence(t, ipa), "local filename differs", "ios-")

	ipa = makeIPA(t, "1.1.0", "1.1.0", ipaOptions{})
	appendZipEntry(t, ipa, "Payload/nexterm.app/NOTICE", []byte("notice"), 0o100644)
	patchLocalZipEntryName(t, ipa, "Payload/nexterm.app/NOTICE", "Payload/nexterm.app/NOTICF")
	expectZipFailure(t, iosEvidence(t, ipa), "local filename differs", "ios-")
}

func TestRejectsAmbiguousFilenames(t *testing.T) {
	for _, nulCount := range []int{1, 4} {
		name := "nexterm" + strings.Repeat("X", nulCount)
		ipa := makeIPA(t, "1.1.0", "1.1.0", ipaOptions{executableName: name})
		patchZipEntryName(t, ipa, "Payload/nexterm.app/"+name, "Payload/nexterm.app/nexterm"+strings.Repeat("\x00", nulCount))
		expectZipFailure(t, iosEvidence(t, ipa), "ambiguous ZIP entry filename", "ios-")
	}
	apk := makeAndroidAPK(t, "AndroidManifest.xmlX", nil)
	patchZipEntryName(t, apk, "AndroidManifest.xmlX", "AndroidManifest.xml\x00")
	expectZipFailure(t, androidEvidence(t, apk, androidEnvironment(t, makeAndroidTools(t, androidTools{}), androidCert)), "ambiguous ZIP entry filename", "android-")
}

func TestIOSPlatformChecks(t *testing.T) {
	plist := setPlistString(plistWithVersions(t, "1.1.0", "1.1.0"), "DTPlatformName", "iphoneos")
	got := iosEvidence(t, makePlistIPA(t, []byte(plist), ipaOptions{executable: makeMachO(7)}))
	expectStatus(t, got, 1)
	if !strings.Contains(got.stderr, "ios-platform") || !got.has("ios-version", "passed", "") || !got.has("ios-platform", "failed", "platforms=7") || !got.has("ios-provisioning-profile", "passed", "") {
		t.Fatalf("simulator: %+v", got.report)
	}
	got = iosEvidence(t, makeIPA(t, "1.1.0", "1.1.0", ipaOptions{executable: makeMachO(2, 2)}))
	expectStatus(t, got, 0)
	if !got.has("ios-platform", "passed", "platforms=2,2") {
		t.Fatalf("fat device: %+v", got.report)
	}
	got = iosEvidence(t, makeIPA(t, "1.1.0", "1.1.0", ipaOptions{executable: makeMachO(2, 7)}))
	if got.status != 1 || !got.has("ios-platform", "failed", "platforms=2,7") {
		t.Fatalf("fat simulator: %+v", got.report)
	}
	got = iosEvidence(t, makeIPA(t, "1.1.0", "1.1.0", ipaOptions{executable: []byte("not a Mach-O executable")}))
	if got.status != 1 || !got.has("ios-version", "passed", "") || !got.has("ios-platform", "failed", "") {
		t.Fatalf("malformed: %+v", got.report)
	}
}

func TestIOSProvisioningProfiles(t *testing.T) {
	got := iosEvidence(t, makeIPA(t, "1.1.0", "1.1.0", ipaOptions{noProfile: true}))
	if got.status != 1 || !got.has("ios-platform", "passed", "") || !got.has("ios-provisioning-profile", "failed", "") {
		t.Fatalf("missing profile: %+v", got.report)
	}
	for name, options := range map[string]profileOptions{
		"development": {getTaskAllow: true, provisionedDevices: true},
		"other app":   {applicationIdentifier: "TEAMID.work.other.app"},
		"enterprise":  {provisionsAllDevices: true},
	} {
		got := iosEvidence(t, makeIPA(t, "1.1.0", "1.1.0", ipaOptions{profile: makeProfile(t, options)}))
		if got.status != 1 || !got.has("ios-provisioning-profile", "failed", "") {
			t.Fatalf("%s: %+v", name, got.report)
		}
	}
	got = iosEvidence(t, makeIPA(t, "1.1.0", "1.1.0", ipaOptions{profile: []byte("not a CMS profile")}))
	if got.status != 1 || !got.has("ios-provisioning-profile", "failed", "") {
		t.Fatalf("malformed CMS: %+v", got.report)
	}
}

func TestIOSVersions(t *testing.T) {
	for _, release := range []string{"1.2.3", "1.2.3-rc.1", "1.2.3+build.5", "1.2.3-rc.1+build.5"} {
		ipa := makeIPA(t, "1.2.3", "42.1", ipaOptions{})
		got := evidence(t, ipa, "mobile-ios", map[string]string{"NEXTERM_RELEASE_VERSION": release, "IOS_BUILD_NUMBER": "42.1"})
		if got.status != 0 || !got.has("ios-version", "passed", "") {
			t.Fatalf("%s: %+v %s", release, got.report, got.stderr)
		}
	}
	for _, versions := range [][2]string{{"1.2.3-rc.1", "42.1"}, {"1.2.3", "42.2"}} {
		got := evidence(t, makeIPA(t, versions[0], versions[1], ipaOptions{}), "mobile-ios", map[string]string{"NEXTERM_RELEASE_VERSION": "1.2.3-rc.1+build.5", "IOS_BUILD_NUMBER": "42.1"})
		if got.status != 1 || !got.has("ios-version", "failed", "") {
			t.Fatalf("%v: %+v", versions, got.report)
		}
	}
	for _, release := range []string{"0.0.0", "1.2.3-rc..1", "1.2.3+build..5", "1.2.3-01", "1.2.3-rc_1"} {
		got := evidence(t, makeIPA(t, "1.2.3", "42.1", ipaOptions{}), "mobile-ios", map[string]string{"NEXTERM_RELEASE_VERSION": release, "IOS_BUILD_NUMBER": "42.1"})
		if got.status != 1 || !got.has("ios-version", "failed", "") {
			t.Fatalf("%s: %+v", release, got.report)
		}
	}
	for _, versions := range [][2]string{{"0.1.0", "1.1.0"}, {"1.1.0", "0.1.0"}} {
		got := iosEvidence(t, makeIPA(t, versions[0], versions[1], ipaOptions{}))
		if got.status != 1 || !strings.Contains(got.stderr, "ios-version") || got.report.Status != "failed" {
			t.Fatalf("%v: %+v", versions, got.report)
		}
	}
}

func TestIOSPlistFormats(t *testing.T) {
	broken := strings.Replace(plistWithVersions(t, "1.1.0", "1.1.0"), "</dict>\n</plist>", "", 1)
	if strings.Contains(broken, "</plist>") {
		t.Fatal("fixture still closes the plist")
	}
	got := iosEvidence(t, makePlistIPA(t, []byte(broken), ipaOptions{}))
	if got.status != 1 || !got.has("ios-version", "failed", "cannot parse") {
		t.Fatalf("malformed XML: %+v", got.report)
	}
	got = iosEvidence(t, makePlistIPA(t, binaryPlistFromXML(t, plistWithVersions(t, "1.1.0", "1.1.0")), ipaOptions{}))
	if got.status != 0 || !got.has("ios-version", "passed", "") {
		t.Fatalf("binary plist: %+v %s", got.report, got.stderr)
	}
	got = iosEvidence(t, makePlistIPA(t, binaryPlistFromXML(t, plistWithVersions(t, "1.1.0", "0.1.0")), ipaOptions{}))
	if got.status != 1 || !got.has("ios-version", "failed", "") {
		t.Fatalf("binary wrong version: %+v", got.report)
	}
	got = iosEvidence(t, makePlistIPA(t, []byte("bplist00 truncated"), ipaOptions{}))
	if got.status != 1 || !got.has("ios-version", "failed", "cannot parse") {
		t.Fatalf("malformed binary: %+v", got.report)
	}
}

func TestIOSExecutableMode(t *testing.T) {
	got := iosEvidence(t, makeIPA(t, "1.1.0", "1.1.0", ipaOptions{executableMode: 0o644}))
	if got.status != 1 || !strings.Contains(got.stderr, "ios-executable-mode") || !got.has("ios-version", "passed", "") || !got.has("ios-executable-mode", "failed", "mode=0644") {
		t.Fatalf("report = %+v", got.report)
	}
}

func TestAndroidChecks(t *testing.T) {
	for _, label := range []string{"Signer #1", "V2 Signer"} {
		got := androidEvidence(t, makeAndroidAPK(t, "", nil), androidEnvironment(t, makeAndroidTools(t, androidTools{signerLabel: label}), androidCert))
		if got.status != 0 || got.report.Status != "passed" || !got.has("android-signature", "passed", "") || !got.has("android-version", "passed", "") {
			t.Fatalf("%s: %+v %s", label, got.report, got.stderr)
		}
	}
	got := androidEvidence(t, makeAndroidAPK(t, "", nil), androidEnvironment(t, makeAndroidTools(t, androidTools{certificate: strings.Repeat("b", 64)}), androidCert))
	if got.status != 1 || !got.has("android-signature", "failed", "") || !got.has("android-version", "passed", "") {
		t.Fatalf("wrong signer: %+v", got.report)
	}
	for _, options := range []androidTools{{versionName: "1.1.1"}, {versionCode: "1001001"}} {
		got := androidEvidence(t, makeAndroidAPK(t, "", nil), androidEnvironment(t, makeAndroidTools(t, options), androidCert))
		if got.status != 1 || !got.has("android-signature", "passed", "") || !got.has("android-version", "failed", "") {
			t.Fatalf("%+v: %+v", options, got.report)
		}
	}
	got = androidEvidence(t, makeAndroidAPK(t, "", nil), androidEnvironment(t, makeAndroidTools(t, androidTools{}), ""))
	if got.status != 1 || !got.has("android-signature", "failed", "") {
		t.Fatalf("unpinned: %+v", got.report)
	}
	version := "1.2.3-rc.1+build.5"
	environment := androidEnvironment(t, makeAndroidTools(t, androidTools{versionName: version, versionCode: "1002003"}), androidCert)
	environment["NEXTERM_RELEASE_VERSION"] = version
	got = androidEvidence(t, makeAndroidAPK(t, "", nil), environment)
	if got.status != 0 || !got.has("android-version", "passed", "") {
		t.Fatalf("prerelease: %+v", got.report)
	}
	got = androidEvidence(t, makeAndroidAPK(t, "", nil), androidEnvironment(t, t.TempDir(), androidCert))
	if got.status != 0 || got.report.Status != "passed" || !got.has("android-signature", "skipped", "not verified") || !got.has("android-version", "skipped", "not verified") {
		t.Fatalf("skipped tools: %+v", got.report)
	}
}

func TestAndroidToolPrefersNewestBuildTools(t *testing.T) {
	sdk := t.TempDir()
	for _, version := range []string{"9.0.0", "35.0.0", "30.0.3"} {
		dir := filepath.Join(sdk, "build-tools", version)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "aapt2"), []byte("#!/bin/sh\n"), 0o755)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ANDROID_HOME", sdk)
	t.Setenv("ANDROID_SDK_ROOT", "")
	if got := androidTool("aapt2"); got != filepath.Join(sdk, "build-tools", "35.0.0", "aapt2") {
		t.Fatalf("androidTool = %s", got)
	}
}

func TestExpectedAndroidVersionCode(t *testing.T) {
	for value, want := range map[string]int64{"1.2.3": 1002003, "1.2.3-rc.1+build.5": 1002003, "2100.0.0": 2100000000} {
		if got, err := expectedAndroidVersionCode(value); err != nil || got != want {
			t.Fatalf("%s = %d, %v", value, got, err)
		}
	}
	for _, value := range []string{"0.0.0", "1.1000.0", "1.0.1000", "2101.0.0", "99999999999999999999.0.0"} {
		if _, err := expectedAndroidVersionCode(value); err == nil {
			t.Fatalf("%s was accepted", value)
		}
	}
}

// binaryPlistFromXML re-encodes an XML plist in the bplist00 format, so the
// binary decoder is checked against the same content as the XML one.
func binaryPlistFromXML(t *testing.T, source string) []byte {
	t.Helper()
	value, err := parseXMLPlist([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	var encoder plistEncoder
	top := encoder.add(value)
	return encoder.bytes(top)
}

type plistEncoder struct{ objects [][]byte }

func (e *plistEncoder) reserve() int {
	e.objects = append(e.objects, nil)
	return len(e.objects) - 1
}

func lengthMarker(kind byte, length int) []byte {
	if length < 15 {
		return []byte{kind<<4 | byte(length)}
	}
	out := []byte{kind<<4 | 0xf, 0x13}
	return binary.BigEndian.AppendUint64(out, uint64(length))
}

func (e *plistEncoder) add(value any) int {
	index := e.reserve()
	switch typed := value.(type) {
	case bool:
		e.objects[index] = []byte{0x08}
		if typed {
			e.objects[index] = []byte{0x09}
		}
	case int64:
		e.objects[index] = binary.BigEndian.AppendUint64([]byte{0x13}, uint64(typed))
	case float64:
		e.objects[index] = binary.BigEndian.AppendUint64([]byte{0x23}, math.Float64bits(typed))
	case string:
		if isASCII([]byte(typed)) {
			e.objects[index] = append(lengthMarker(0x5, len(typed)), typed...)
			break
		}
		units := utf16.Encode([]rune(typed))
		out := lengthMarker(0x6, len(units))
		for _, unit := range units {
			out = binary.BigEndian.AppendUint16(out, unit)
		}
		e.objects[index] = out
	case []byte:
		e.objects[index] = append(lengthMarker(0x4, len(typed)), typed...)
	case []any:
		refs := make([]int, len(typed))
		for i, child := range typed {
			refs[i] = e.add(child)
		}
		e.objects[index] = appendRefs(lengthMarker(0xa, len(refs)), refs)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		refs := make([]int, 0, 2*len(keys))
		for _, key := range keys {
			refs = append(refs, e.add(key))
		}
		for _, key := range keys {
			refs = append(refs, e.add(typed[key]))
		}
		e.objects[index] = appendRefs(lengthMarker(0xd, len(keys)), refs)
	default:
		e.objects[index] = append(lengthMarker(0x5, len(fmt.Sprint(typed))), fmt.Sprint(typed)...)
	}
	return index
}

func appendRefs(out []byte, refs []int) []byte {
	for _, ref := range refs {
		out = binary.BigEndian.AppendUint16(out, uint16(ref))
	}
	return out
}

func (e *plistEncoder) bytes(top int) []byte {
	out := []byte("bplist00")
	offsets := make([]uint64, len(e.objects))
	for index, object := range e.objects {
		offsets[index] = uint64(len(out))
		out = append(out, object...)
	}
	table := uint64(len(out))
	for _, offset := range offsets {
		out = binary.BigEndian.AppendUint32(out, uint32(offset))
	}
	trailer := make([]byte, 32)
	trailer[6], trailer[7] = 4, 2
	binary.BigEndian.PutUint64(trailer[8:], uint64(len(e.objects)))
	binary.BigEndian.PutUint64(trailer[16:], uint64(top))
	binary.BigEndian.PutUint64(trailer[24:], table)
	return append(out, trailer...)
}

func privacyELF(machine uint16) []byte {
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 3)
	binary.LittleEndian.PutUint16(data[18:], machine)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint16(data[52:], 64)
	return data
}
