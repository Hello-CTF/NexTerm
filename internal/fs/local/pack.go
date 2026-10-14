package local

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
)

// PackDownload writes the directory tree at remotePath as a gzipped tar to
// localPath. Both sides are local, so the archive is staged in the system
// temp dir, moved through the shared download path, and removed afterwards.
func (f *FileSystem) PackDownload(ctx context.Context, remotePath, localPath string, options sshfs.TransferOptions) (int64, error) {
	root := real(remotePath)
	info, err := os.Stat(root)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("pack source %s is not a directory", root)
	}
	temporary, err := createPackArchive(ctx, root)
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(temporary) }()
	return Download(ctx, f, temporary, localPath, TransferOptions{
		Progress: func(progress Progress) {
			if options.Progress != nil {
				options.Progress(sshfs.Progress{Transferred: progress.Transferred, Total: progress.Total, Done: progress.Done})
			}
		},
	})
}

// createPackArchive tars root (entries prefixed with its base name, matching
// tar -C <parent> -czf <out> -- <base>) into a fresh temp file and returns
// its path; any failure removes the partial archive.
func createPackArchive(ctx context.Context, root string) (string, error) {
	file, err := os.CreateTemp("", "nexterm-pack-*.tar.gz")
	if err != nil {
		return "", err
	}
	name := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(name)
		}
	}()
	compressed := gzip.NewWriter(file)
	writer := tar.NewWriter(compressed)
	parent := filepath.Dir(root)
	walkErr := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if entry.Type()&fs.ModeSymlink != 0 {
			link, err = os.Readlink(current)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(parent, current)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		source, err := os.Open(current)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr == nil {
		if err := writer.Close(); err != nil {
			walkErr = err
		} else if err := compressed.Close(); err != nil {
			walkErr = err
		} else if err := file.Close(); err != nil {
			walkErr = err
		}
	}
	if walkErr != nil {
		return "", walkErr
	}
	committed = true
	return name, nil
}
