// Command archive builds and inspects the reproducible release archives used by
// the packaging scripts:
//
//	go run ./scripts/archive tar ARTIFACT SOURCE MEMBER EPOCH
//	go run ./scripts/archive zip ARTIFACT FILE MEMBER EPOCH
//	go run ./scripts/archive copy-tree SOURCE DESTINATION
//	go run ./scripts/archive list ARCHIVE
//	go run ./scripts/archive extract ARCHIVE DESTINATION
//
// tar writes SOURCE/MEMBER (a file or directory tree) as a gzip-compressed tar
// whose entries are sorted by path, owned by 0:0 and stamped with EPOCH, so the
// same input always yields the same bytes. zip stores one regular file the same
// way. Symbolic links and other non-regular files are rejected.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "archive: %v\n", err)
		os.Exit(1)
	}
}

const usage = "usage: archive tar ARTIFACT SOURCE MEMBER EPOCH | zip ARTIFACT FILE MEMBER EPOCH | copy-tree SOURCE DESTINATION | list ARCHIVE | extract ARCHIVE DESTINATION"

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch {
	case args[0] == "tar" && len(args) == 5:
		epoch, err := parseEpoch(args[4])
		if err != nil {
			return err
		}
		return createTar(args[1], args[2], args[3], epoch)
	case args[0] == "zip" && len(args) == 5:
		epoch, err := parseEpoch(args[4])
		if err != nil {
			return err
		}
		return createZip(args[1], args[2], args[3], epoch)
	case args[0] == "copy-tree" && len(args) == 3:
		return copyTree(args[1], args[2])
	case args[0] == "list" && len(args) == 2:
		names, err := listArchive(args[1])
		if err != nil {
			return err
		}
		for _, name := range names {
			fmt.Fprintln(stdout, name)
		}
		return nil
	case args[0] == "extract" && len(args) == 3:
		return extractZip(args[1], args[2])
	}
	return errors.New(usage)
}

func parseEpoch(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, fmt.Errorf("invalid epoch %q", value)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

// treePaths returns root/member followed by its descendants, sorted by their
// slash-separated path relative to root.
func treePaths(root, member string) ([]string, error) {
	base := filepath.Join(root, member)
	info, err := os.Lstat(base)
	if err != nil {
		return nil, fmt.Errorf("archive input does not exist: %s", base)
	}
	paths := []string{member}
	if info.IsDir() {
		var children []string
		err := filepath.WalkDir(base, func(current string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if current == base {
				return nil
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			children = append(children, filepath.ToSlash(relative))
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(children)
		paths = append(paths, children...)
	}
	return paths, nil
}

func createTar(artifact, source, member string, epoch time.Time) error {
	paths, err := treePaths(source, member)
	if err != nil {
		return err
	}
	file, err := os.Create(artifact)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	compressed.ModTime = epoch
	writer := tar.NewWriter(compressed)
	for _, relative := range paths {
		local := filepath.Join(source, filepath.FromSlash(relative))
		info, err := os.Lstat(local)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: relative, ModTime: epoch, Uid: 0, Gid: 0}
		switch {
		case info.IsDir():
			header.Typeflag = tar.TypeDir
			header.Name += "/"
			header.Mode = 0o755
		case info.Mode().IsRegular():
			header.Typeflag = tar.TypeReg
			header.Mode = 0o644
			if info.Mode().Perm()&0o111 != 0 {
				header.Mode = 0o755
			}
			header.Size = info.Size()
		default:
			return fmt.Errorf("archive input must be a regular file or directory: %s", local)
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg {
			if err := copyFileInto(writer, local, info.Size()); err != nil {
				return err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(artifact, 0o644)
}

func copyFileInto(writer io.Writer, local string, size int64) error {
	input, err := os.Open(local)
	if err != nil {
		return err
	}
	defer input.Close()
	written, err := io.Copy(writer, input)
	if err != nil {
		return err
	}
	if written != size {
		return fmt.Errorf("%s changed size while archiving", local)
	}
	return nil
}

func createZip(artifact, source, member string, epoch time.Time) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("zip input must be a regular file: %s", source)
	}
	file, err := os.Create(artifact)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: member, Method: zip.Deflate}
	// Only the MS-DOS timestamp is written; setting Modified would add an
	// extended-timestamp extra field.
	header.ModifiedDate, header.ModifiedTime = dosDateTime(epoch)
	header.SetMode(0o755)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	if err := copyFileInto(entry, source, info.Size()); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(artifact, 0o644)
}

func dosDateTime(t time.Time) (uint16, uint16) {
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	date := uint16(t.Day() + int(t.Month())<<5 + (t.Year()-1980)<<9)
	clock := uint16(t.Second()/2 + t.Minute()<<5 + t.Hour()<<11)
	return date, clock
}

func copyTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(destination, 0o755); err != nil {
		return err
	}
	var relatives []string
	err := filepath.WalkDir(source, func(current string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current == source {
			return nil
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		relatives = append(relatives, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(relatives)
	for _, relative := range relatives {
		local := filepath.Join(source, filepath.FromSlash(relative))
		target := filepath.Join(destination, filepath.FromSlash(relative))
		info, err := os.Lstat(local)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("frontend output contains a symbolic link: %s", local)
		case info.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			if err := os.Chmod(target, 0o755); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := copyRegular(local, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("frontend output contains a non-regular file: %s", local)
		}
	}
	return nil
}

func copyRegular(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Chmod(target, 0o644)
}

func listArchive(name string) ([]string, error) {
	if strings.HasSuffix(name, ".zip") {
		reader, err := zip.OpenReader(name)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		names := make([]string, 0, len(reader.File))
		for _, file := range reader.File {
			names = append(names, file.Name)
		}
		return names, nil
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	reader := tar.NewReader(compressed)
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		names = append(names, header.Name)
	}
}

// extractZip unpacks regular files and directories from a zip, refusing any
// entry that would land outside destination.
func extractZip(name, destination string) error {
	reader, err := zip.OpenReader(name)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		clean := path.Clean(file.Name)
		if !fs.ValidPath(clean) {
			return fmt.Errorf("unsafe zip entry path: %q", file.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(clean))
		mode := file.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := extractEntry(file, target, mode.Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported zip entry type: %s", file.Name)
		}
	}
	return nil
}

func extractEntry(file *zip.File, target string, perm fs.FileMode) error {
	input, err := file.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	if perm == 0 {
		perm = 0o644
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}
