package local

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// ErrUnsafeArchive reports an archive entry that would land outside the
// extraction directory or that extraction refuses, such as a hard link.
var ErrUnsafeArchive = errors.New("archive entry is unsafe to extract")

// Extract unpacks an archive next to it, into a directory named after it
// without the extension, like the remote tar and unzip commands do. Entries
// never land outside that directory: absolute paths, ".." segments, hard links
// and writes through a symbolic link are refused. A format the standard
// library cannot read (.tar.xz) reports base.ErrUnsupported.
func (f *FileSystem) Extract(ctx context.Context, archivePath string) (string, error) {
	source := real(archivePath)
	name := filepath.Base(source)
	baseName := sshfs.StripArchiveExtension(name)
	if baseName == "" || baseName == "." || baseName == ".." || baseName == name {
		return "", fmt.Errorf("unsupported archive format %q", name)
	}
	target := filepath.Join(filepath.Dir(source), baseName)
	resultPath := path.Join(path.Dir(filepath.ToSlash(archivePath)), baseName)
	if strings.Contains(archivePath, `\`) {
		resultPath = filepath.Join(filepath.Dir(archivePath), baseName)
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".zip") {
		return resultPath, extractZip(ctx, source, target)
	}
	file, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var reader io.Reader = file
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		compressed, err := gzip.NewReader(file)
		if err != nil {
			return "", err
		}
		defer compressed.Close()
		reader = compressed
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"), strings.HasSuffix(lower, ".tbz"):
		reader = bzip2.NewReader(file)
	case strings.HasSuffix(lower, ".tar"):
	default:
		return "", fmt.Errorf("%w: %s", base.ErrUnsupported, name)
	}
	return resultPath, extractTar(ctx, tar.NewReader(reader), target)
}

func extractTar(ctx context.Context, archive *tar.Reader, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		destination, err := extractionPath(target, header.Name)
		if err != nil {
			return err
		}
		if destination == "" {
			continue
		}
		mode := fs.FileMode(header.Mode).Perm()
		switch header.Typeflag {
		case tar.TypeDir:
			if err := mkdirInside(target, destination, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeInside(target, destination, mode, archive); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := symlinkInside(target, destination, header.Linkname); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			return fmt.Errorf("%w: %s (type %c)", ErrUnsafeArchive, header.Name, header.Typeflag)
		}
	}
}

func extractZip(ctx context.Context, source, target string) error {
	archive, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer archive.Close()
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		destination, err := extractionPath(target, entry.Name)
		if err != nil {
			return err
		}
		if destination == "" {
			continue
		}
		mode := entry.Mode()
		switch {
		case mode.IsDir():
			if err := mkdirInside(target, destination, mode.Perm()|0o700); err != nil {
				return err
			}
		case mode&fs.ModeSymlink != 0:
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			link, err := io.ReadAll(io.LimitReader(reader, 4096))
			_ = reader.Close()
			if err != nil {
				return err
			}
			if err := symlinkInside(target, destination, string(link)); err != nil {
				return err
			}
		case mode.IsRegular():
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			err = writeInside(target, destination, mode.Perm(), reader)
			_ = reader.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %s", ErrUnsafeArchive, entry.Name)
		}
	}
	return nil
}

// extractionPath maps an entry name into target, refusing absolute names and
// names that climb out of it; the archive's root entry maps to "".
func extractionPath(target, name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("%w: absolute path %s", ErrUnsafeArchive, name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: %s climbs out of the target", ErrUnsafeArchive, name)
		}
	}
	cleaned := path.Clean(name)
	if cleaned == "." {
		return "", nil
	}
	return filepath.Join(target, filepath.FromSlash(cleaned)), nil
}

// checkParents refuses a destination whose parent directories inside target
// include a symbolic link, so an entry cannot write through a link the
// archive created earlier.
func checkParents(target, destination string) error {
	relative, err := filepath.Rel(target, filepath.Dir(destination))
	if err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	current := target
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s goes through a symbolic link", ErrUnsafeArchive, destination)
		}
	}
	return nil
}

func mkdirInside(target, destination string, mode fs.FileMode) error {
	if err := checkParents(target, destination); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symbolic link", ErrUnsafeArchive, destination)
	}
	return os.MkdirAll(destination, mode)
}

func writeInside(target, destination string, mode fs.FileMode, data io.Reader) error {
	if err := mkdirInside(target, filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		// Replace the link itself, never the file it points to.
		if err := os.Remove(destination); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode|0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, data)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// symlinkInside creates a link only when its target stays inside the
// extraction directory.
func symlinkInside(target, destination, link string) error {
	if err := mkdirInside(target, filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	resolved := link
	if !filepath.IsAbs(link) {
		resolved = filepath.Join(filepath.Dir(destination), link)
	}
	relative, err := filepath.Rel(target, filepath.Clean(resolved))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(link) {
		return fmt.Errorf("%w: link %s points outside the target", ErrUnsafeArchive, destination)
	}
	if _, err := os.Lstat(destination); err == nil {
		if err := os.Remove(destination); err != nil {
			return err
		}
	}
	return os.Symlink(link, destination)
}
