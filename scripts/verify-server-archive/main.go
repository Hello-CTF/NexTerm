// Command verify-server-archive proves that a Linux server release archive
// carries the exact staged CI server binary:
//
//	go run ./scripts/verify-server-archive <archive.tar.gz> <staged-binary>
//
// The staged binary must match its <binary>.manifest.json digest, and the
// archive's single <root>/nexterm-server member must be a regular file with the
// same bytes. Members are judged by the path a real extractor writes to, so
// duplicates, "."/empty segments, links on the parent path and members under
// non-directory paths are rejected instead of letting another member replace
// the evidenced bytes during unpacking.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const mainBinary = "nexterm-server"

func main() {
	if len(os.Args) != 3 {
		fail(errors.New("usage: verify-server-archive <archive.tar.gz> <staged-binary>"))
	}
	digest, err := verify(os.Args[1], os.Args[2])
	if err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "verify-server-archive: %s carries the staged main binary sha256:%s\n", filepath.Base(os.Args[1]), digest)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "verify-server-archive: %v\n", err)
	os.Exit(1)
}

type manifest struct {
	Artifact struct {
		SHA256    string `json:"sha256"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"artifact"`
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func verify(archive, binary string) (string, error) {
	for _, file := range []struct{ name, path string }{{"archive", archive}, {"staged binary", binary}} {
		if info, err := os.Stat(file.path); err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s does not exist: %s", file.name, file.path)
		}
	}
	manifestPath := binary + ".manifest.json"
	if info, err := os.Stat(manifestPath); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("staged binary manifest is missing: %s", manifestPath)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", err
	}
	var staged manifest
	if err := json.Unmarshal(raw, &staged); err != nil {
		return "", fmt.Errorf("staged binary manifest is not valid JSON: %v", err)
	}
	if !sha256Pattern.MatchString(staged.Artifact.SHA256) {
		return "", errors.New("staged binary manifest lacks a SHA-256 artifact digest")
	}
	binaryDigest, binarySize, err := fileDigest(binary)
	if err != nil {
		return "", err
	}
	if binaryDigest != staged.Artifact.SHA256 {
		return "", fmt.Errorf("staged binary sha256 does not match its CI manifest: staged=%s, manifest=%s", binaryDigest, staged.Artifact.SHA256)
	}
	evidence, err := inspect(archive, mainBinary)
	if err != nil {
		return "", err
	}
	if evidence.sha256 != binaryDigest {
		return "", fmt.Errorf("server archive main binary does not match the staged CI binary: archive=%s, staged=%s", evidence.sha256, binaryDigest)
	}
	if evidence.size != binarySize || evidence.size != staged.Artifact.SizeBytes {
		return "", fmt.Errorf("server archive main binary size evidence mismatch: archive=%d, staged=%d, manifest=%d", evidence.size, binarySize, staged.Artifact.SizeBytes)
	}
	return binaryDigest, nil
}

func fileDigest(name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

type memberEvidence struct {
	root, member, sha256 string
	size                 int64
}

type member struct {
	header    *tar.Header
	canonical string
	digest    string
	read      int64
}

// canonical returns the extraction path for a member, rejecting any header
// name whose string differs from where a real extractor would write it.
func canonical(header *tar.Header) (string, error) {
	name := header.Name
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("server archive member has an unsafe path: %s", name)
	}
	parts := strings.Split(name, "/")
	if parts[len(parts)-1] == "" {
		// Only a directory may carry a trailing slash: a regular file or link
		// with one extracts as a directory, so the unpack would diverge.
		if header.Typeflag != tar.TypeDir {
			return "", fmt.Errorf("server archive member has a trailing slash but is not a directory: %s", name)
		}
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("server archive member has an empty path: %s", name)
	}
	for _, part := range parts {
		switch part {
		case "":
			return "", fmt.Errorf("server archive member has an empty path segment: %s", name)
		case ".":
			return "", fmt.Errorf("server archive member has a '.' path segment: %s", name)
		case "..":
			return "", fmt.Errorf("server archive member has an unsafe path: %s", name)
		}
	}
	return strings.Join(parts, "/"), nil
}

func readMembers(archive string) ([]member, error) {
	file, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("cannot open the server archive: %v", err)
	}
	reader := tar.NewReader(compressed)
	var members []member
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return members, nil
		}
		if err != nil {
			return nil, fmt.Errorf("cannot open the server archive: %v", err)
		}
		name, err := canonical(header)
		if err != nil {
			return nil, err
		}
		entry := member{header: header, canonical: name}
		if header.Typeflag == tar.TypeReg {
			hash := sha256.New()
			read, err := io.Copy(hash, reader)
			if err != nil {
				return nil, fmt.Errorf("cannot read %s: %v", header.Name, err)
			}
			entry.digest, entry.read = hex.EncodeToString(hash.Sum(nil)), read
		}
		members = append(members, entry)
	}
}

func inspect(archive, basename string) (memberEvidence, error) {
	members, err := readMembers(archive)
	if err != nil {
		return memberEvidence{}, err
	}
	if len(members) == 0 {
		return memberEvidence{}, errors.New("server archive is empty")
	}
	byPath := map[string]member{}
	roots := map[string]bool{}
	for _, entry := range members {
		if _, duplicate := byPath[entry.canonical]; duplicate {
			return memberEvidence{}, errors.New("server archive contains duplicate member names")
		}
		byPath[entry.canonical] = entry
		roots[strings.SplitN(entry.canonical, "/", 2)[0]] = true
	}
	if len(roots) != 1 {
		names := make([]string, 0, len(roots))
		for name := range roots {
			names = append(names, name)
		}
		sort.Strings(names)
		return memberEvidence{}, fmt.Errorf("server archive must contain exactly one top-level directory; found: %s", strings.Join(names, ", "))
	}
	var root string
	for name := range roots {
		root = name
	}
	memberName := root + "/" + basename
	main, ok := byPath[memberName]
	if !ok {
		return memberEvidence{}, fmt.Errorf("server archive is missing the main binary member: %s", memberName)
	}
	if main.header.Typeflag != tar.TypeReg {
		return memberEvidence{}, fmt.Errorf("server archive main binary member is not a regular file: %s", memberName)
	}
	parents := strings.Split(memberName, "/")
	for index := 1; index < len(parents); index++ {
		ancestor := strings.Join(parents[:index], "/")
		if entry, ok := byPath[ancestor]; ok && (entry.header.Typeflag == tar.TypeSymlink || entry.header.Typeflag == tar.TypeLink) {
			return memberEvidence{}, fmt.Errorf("server archive redirects the main binary parent path through a link: %s", ancestor)
		}
	}
	// A member may only live under explicit directories; implicitly missing
	// directories stay legal.
	for _, entry := range members {
		parts := strings.Split(entry.canonical, "/")
		for index := 1; index < len(parts); index++ {
			ancestor := strings.Join(parts[:index], "/")
			if parent, ok := byPath[ancestor]; ok && parent.header.Typeflag != tar.TypeDir {
				return memberEvidence{}, fmt.Errorf("server archive member is located under a non-directory path: %s", ancestor)
			}
		}
	}
	if main.read != main.header.Size {
		return memberEvidence{}, fmt.Errorf("main binary member size differs from its tar header: %d != %d", main.read, main.header.Size)
	}
	return memberEvidence{root: root, member: memberName, sha256: main.digest, size: main.read}, nil
}
