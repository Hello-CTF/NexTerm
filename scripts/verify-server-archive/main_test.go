package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var mainBytes = []byte("server main binary fixture")

type entry struct {
	name     string
	typeflag byte
	data     []byte
	linkname string
}

func file(name string, data []byte) entry {
	return entry{name: name, typeflag: tar.TypeReg, data: data}
}

func writeTar(t *testing.T, path string, entries ...entry) string {
	t.Helper()
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	compressed := gzip.NewWriter(out)
	writer := tar.NewWriter(compressed)
	for _, item := range entries {
		header := &tar.Header{Name: item.name, Typeflag: item.typeflag, Mode: 0o644, Linkname: item.linkname, Size: int64(len(item.data))}
		if item.typeflag == tar.TypeDir {
			header.Mode = 0o755
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(item.data) > 0 {
			if _, err := writer.Write(item.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeRawRegular writes one regular-file header by hand, for names that
// archive/tar refuses to encode (such as a trailing slash on a regular file).
func writeRawRegular(t *testing.T, path, name string, data []byte) string {
	t.Helper()
	header := make([]byte, 512)
	copy(header[0:100], name)
	copy(header[100:108], fmt.Sprintf("%07o\x00", 0o644))
	copy(header[108:116], fmt.Sprintf("%07o\x00", 0))
	copy(header[116:124], fmt.Sprintf("%07o\x00", 0))
	copy(header[124:136], fmt.Sprintf("%011o\x00", len(data)))
	copy(header[136:148], fmt.Sprintf("%011o\x00", 0))
	header[156] = tar.TypeReg
	copy(header[257:265], "ustar\x0000")
	copy(header[148:156], "        ")
	sum := 0
	for _, value := range header {
		sum += int(value)
	}
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", sum))
	var raw bytes.Buffer
	raw.Write(header)
	raw.Write(data)
	raw.Write(make([]byte, (512-len(data)%512)%512+1024))
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	compressed := gzip.NewWriter(out)
	if _, err := compressed.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// intact mirrors the release layout: a root directory with the server and helpers.
func intact(t *testing.T, dir, root string, main []byte) string {
	return writeTar(t, filepath.Join(dir, root+".tar.gz"),
		entry{name: root + "/", typeflag: tar.TypeDir},
		file(root+"/nexterm-server", main),
		file(root+"/nexterm-helper-linux-amd64", []byte("helper fixture")),
		file(root+"/web-index-placeholder", []byte("placeholder")),
	)
}

func stagedBinary(t *testing.T, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, "nexterm-server-linux-amd64")
	if err := os.WriteFile(binary, mainBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, binary, mainBytes)
	return binary
}

func writeManifest(t *testing.T, binary string, data []byte) {
	t.Helper()
	sum := sha256.Sum256(data)
	raw, _ := json.Marshal(map[string]any{"artifact": map[string]any{"size_bytes": len(data), "sha256": hex.EncodeToString(sum[:])}})
	if err := os.WriteFile(binary+".manifest.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func expectError(t *testing.T, archive, binary, pattern string) {
	t.Helper()
	_, err := verify(archive, binary)
	if err == nil || !strings.Contains(err.Error(), pattern) {
		t.Fatalf("verify(%s) error = %v, want %q", filepath.Base(archive), err, pattern)
	}
}

func TestAcceptsIntactArchiveAndRejectsTampering(t *testing.T) {
	dir := t.TempDir()
	binary := stagedBinary(t, dir)
	archive := intact(t, dir, "nexterm-server_linux_amd64_1.2.3", mainBytes)
	digest, err := verify(archive, binary)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(mainBytes); digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %s", digest)
	}
	tampered := intact(t, dir, "nexterm-server_linux_amd64_1.2.3-tampered", []byte("server main binary fixture!"))
	expectError(t, tampered, binary, "does not match the staged CI binary")

	if err := os.WriteFile(binary, append(append([]byte{}, mainBytes...), "tampered"...), 0o755); err != nil {
		t.Fatal(err)
	}
	expectError(t, archive, binary, "does not match its CI manifest")
}

func TestRejectsLinksAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	binary := stagedBinary(t, dir)
	root := "nexterm-server_linux_amd64_1.2.3"
	cases := []struct {
		label, want string
		entries     []entry
	}{
		// Following the symlink would yield matching bytes; only the type check rejects it.
		{"symlink-target", "not a regular file", []entry{{name: root + "/nexterm-server", typeflag: tar.TypeSymlink, linkname: binary}}},
		{"parent-symlink", "parent path", []entry{{name: root, typeflag: tar.TypeSymlink, linkname: "payload-dir"}, file(root+"/nexterm-server", mainBytes)}},
		{"hardlink", "not a regular file", []entry{{name: root + "/nexterm-server", typeflag: tar.TypeLink, linkname: binary}}},
		{"duplicates", "duplicate member names", []entry{file(root+"/nexterm-server", mainBytes), file(root+"/nexterm-server", append(append([]byte{}, mainBytes...), "tampered"...))}},
	}
	for _, test := range cases {
		t.Run(test.label, func(t *testing.T) {
			expectError(t, writeTar(t, filepath.Join(dir, test.label+".tar.gz"), test.entries...), binary, test.want)
		})
	}
	if _, err := verify(intact(t, dir, root, mainBytes), binary); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsNonCanonicalMembers(t *testing.T) {
	dir := t.TempDir()
	binary := stagedBinary(t, dir)
	root := "nexterm-server_linux_amd64_1.2.3"
	tampered := append(append([]byte{}, mainBytes...), "tampered"...)
	// A second member that only normalizes onto the main path would overwrite it on unpack.
	expectError(t, writeTar(t, filepath.Join(dir, "dot.tar.gz"), file(root+"/nexterm-server", mainBytes), file(root+"/./nexterm-server", tampered)), binary, "'.' path segment")
	expectError(t, writeTar(t, filepath.Join(dir, "empty.tar.gz"), file(root+"/nexterm-server", mainBytes), file(root+"//nexterm-server", tampered)), binary, "empty path segment")
	// A regular member with a trailing slash extracts as a directory, not the binary.
	expectError(t, writeRawRegular(t, filepath.Join(dir, "slash.tar.gz"), root+"/nexterm-server/", mainBytes), binary, "trailing slash")
	expectError(t, writeTar(t, filepath.Join(dir, "abs.tar.gz"), file("/"+root+"/nexterm-server", mainBytes)), binary, "unsafe path")
	expectError(t, writeTar(t, filepath.Join(dir, "dotdot.tar.gz"), file(root+"/../nexterm-server", mainBytes)), binary, "unsafe path")
}

func TestRejectsMembersUnderNonDirectoryPaths(t *testing.T) {
	dir := t.TempDir()
	binary := stagedBinary(t, dir)
	root := "nexterm-server_linux_amd64_1.2.3"
	main := file(root+"/nexterm-server", mainBytes)
	rootFile := file(root, []byte("not a directory"))
	child := file(root+"/nexterm-server/child", []byte("child"))
	// Archive order must not matter.
	for label, entries := range map[string][]entry{
		"root-regular-first": {rootFile, main},
		"root-regular-last":  {main, rootFile},
		"child-first":        {child, main},
		"child-last":         {main, child},
	} {
		expectError(t, writeTar(t, filepath.Join(dir, label+".tar.gz"), entries...), binary, "non-directory")
	}
	// Implicitly missing directory members stay legal.
	if _, err := verify(writeTar(t, filepath.Join(dir, "implicit.tar.gz"), main), binary); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsMissingMembersManifestsAndLayouts(t *testing.T) {
	dir := t.TempDir()
	binary := stagedBinary(t, dir)
	root := "nexterm-server_linux_amd64_1.2.3"
	expectError(t, writeTar(t, filepath.Join(dir, "missing.tar.gz"), entry{name: root + "/", typeflag: tar.TypeDir}), binary, "missing the main binary member")
	expectError(t, writeTar(t, filepath.Join(dir, "none.tar.gz")), binary, "server archive is empty")
	expectError(t, writeTar(t, filepath.Join(dir, "roots.tar.gz"), file(root+"/nexterm-server", mainBytes), file("other/x", []byte("x"))), binary, "exactly one top-level directory")

	good := intact(t, dir, root, mainBytes)
	if err := os.Remove(binary + ".manifest.json"); err != nil {
		t.Fatal(err)
	}
	expectError(t, good, binary, "manifest is missing")
	if err := os.WriteFile(binary+".manifest.json", []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectError(t, good, binary, "not valid JSON")
	if err := os.WriteFile(binary+".manifest.json", []byte(`{"artifact":{"size_bytes":26,"sha256":"`+strings.Repeat("0", 64)+`"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	expectError(t, good, binary, "does not match its CI manifest")
	writeManifest(t, binary, mainBytes)
	expectError(t, filepath.Join(dir, "absent.tar.gz"), binary, "archive does not exist")
	if err := os.WriteFile(filepath.Join(dir, "plain.tar.gz"), []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectError(t, filepath.Join(dir, "plain.tar.gz"), binary, "cannot open the server archive")
}

func TestManifestSizeMustMatch(t *testing.T) {
	dir := t.TempDir()
	binary := stagedBinary(t, dir)
	sum := sha256.Sum256(mainBytes)
	raw, _ := json.Marshal(map[string]any{"artifact": map[string]any{"size_bytes": len(mainBytes) + 1, "sha256": hex.EncodeToString(sum[:])}})
	if err := os.WriteFile(binary+".manifest.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
	expectError(t, intact(t, dir, "nexterm-server_linux_amd64_1.2.3", mainBytes), binary, "size evidence mismatch")
}
