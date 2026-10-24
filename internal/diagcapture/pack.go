package diagcapture

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

// Entry describes one finished or running capture folder.
type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	TimeMs    int64  `json:"timeMs"`
	// Archive is the packed zip path when one exists next to the folder.
	Archive string `json:"archive,omitempty"`
}

// List returns capture folders, newest first.
func List(paths platform.Paths) ([]Entry, error) {
	root := Root(paths)
	items, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Entry{}, nil
		}
		return nil, err
	}
	entries := []Entry{}
	for _, item := range items {
		if !item.IsDir() || item.Type()&fs.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(root, item.Name())
		info, err := item.Info()
		if err != nil {
			continue
		}
		entry := Entry{Name: item.Name(), Path: dir, SizeBytes: dirSize(dir), TimeMs: info.ModTime().UnixMilli()}
		if _, err := os.Stat(dir + ".zip"); err == nil {
			entry.Archive = dir + ".zip"
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name > entries[j].Name })
	return entries, nil
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ResolveCapture validates that dir names a capture folder directly under the capture root and returns its
// cleaned path. Anything else (nested paths, traversal, symlinks, other folders) is rejected.
func ResolveCapture(paths platform.Paths, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("capture folder is empty")
	}
	root, err := filepath.Abs(Root(paths))
	if err != nil {
		return "", err
	}
	candidate := filepath.Clean(dir)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	if filepath.Dir(candidate) != root || filepath.Base(candidate) == ".." || filepath.Base(candidate) == "." {
		return "", fmt.Errorf("capture folder must be directly under %s", root)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return "", errors.New("capture folder is not a plain directory")
	}
	return candidate, nil
}

// Pack zips a capture folder into <root>/<name>.zip and returns the archive path.
func Pack(paths platform.Paths, dir string) (string, error) {
	src, err := ResolveCapture(paths, dir)
	if err != nil {
		return "", err
	}
	target := src + ".zip"
	// A unique temp name keeps concurrent packs of the same capture from corrupting each other.
	out, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*.tmp")
	if err != nil {
		return "", err
	}
	tmp := out.Name()
	zw := zip.NewWriter(out)
	base := filepath.Base(src)
	walkErr := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = base + "/" + filepath.ToSlash(rel)
		header.Method = zip.Deflate
		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	closeErr := zw.Close()
	if err := out.Close(); closeErr == nil {
		closeErr = err
	}
	if walkErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return "", errors.Join(walkErr, closeErr)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return target, nil
}
