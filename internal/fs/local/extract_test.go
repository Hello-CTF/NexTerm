package local

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type tarEntry struct {
	name     string
	typeflag byte
	body     string
	link     string
}

func writeTarGz(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Mode: 0o644, Size: int64(len(entry.body)), Linkname: entry.link}
		if entry.typeflag == tar.TypeDir {
			header.Mode = 0o755
		}
		if entry.typeflag != tar.TypeReg {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.typeflag == tar.TypeReg {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
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
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExtractUnpacksIntoADirectoryNamedAfterTheArchive(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "site.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "./", typeflag: tar.TypeDir},
		{name: "conf/", typeflag: tar.TypeDir},
		{name: "conf/app.yml", typeflag: tar.TypeReg, body: "port: 80"},
		{name: "current", typeflag: tar.TypeSymlink, link: "conf/app.yml"},
	})
	target, err := New().Extract(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "site"); target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "site", "conf", "app.yml")); err != nil || string(data) != "port: 80" {
		t.Fatalf("content = %q, %v", data, err)
	}
	if runtime.GOOS != "windows" {
		if link, err := os.Readlink(filepath.Join(dir, "site", "current")); err != nil || link != "conf/app.yml" {
			t.Fatalf("link = %q, %v", link, err)
		}
	}
}

func TestExtractUnpacksZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "docs.zip")
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("guide/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Extract(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "docs", "guide", "readme.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("content = %q, %v", data, err)
	}
}

// Nothing in an archive may land outside its directory, whatever path or
// link trick an entry uses.
func TestExtractRefusesEntriesThatEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	for name, entries := range map[string][]tarEntry{
		"parent segment":       {{name: "../escaped.txt", typeflag: tar.TypeReg, body: "x"}},
		"nested parent":        {{name: "a/../../escaped.txt", typeflag: tar.TypeReg, body: "x"}},
		"absolute path":        {{name: "/tmp/escaped.txt", typeflag: tar.TypeReg, body: "x"}},
		"link outside":         {{name: "out", typeflag: tar.TypeSymlink, link: "../.."}},
		"absolute link":        {{name: "out", typeflag: tar.TypeSymlink, link: "/etc"}},
		"hard link":            {{name: "hard", typeflag: tar.TypeLink, link: "/etc/passwd"}},
		"write through a link": {{name: "inside", typeflag: tar.TypeDir}, {name: "via", typeflag: tar.TypeSymlink, link: "inside"}, {name: "via/file.txt", typeflag: tar.TypeReg, body: "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "evil.tar.gz")
			writeTarGz(t, archive, entries)
			_, err := New().Extract(context.Background(), archive)
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("extract = %v, want it refused as unsafe", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("an entry landed outside the extraction directory")
			}
		})
	}
}

func TestExtractReportsFormatsItCannotRead(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "data.tar.xz")
	if err := os.WriteFile(archive, []byte("xz"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Extract(context.Background(), archive); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("extract .tar.xz = %v, want unsupported so the caller can fall back", err)
	}
}
