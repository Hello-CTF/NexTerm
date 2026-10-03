package winrm

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const BackupSuffix = ".nexterm-bak"

type Executor interface {
	Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error)
}

type FileSystem struct {
	executor Executor
}

func New(executor Executor) *FileSystem {
	return &FileSystem{executor: executor}
}

func (f *FileSystem) execute(ctx context.Context, script string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result, err := f.executor.Exec(ctx, script, base.ExecOptions{
		Limits: base.OutputLimits{Stdout: -1, Stderr: -1},
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode == nil {
		return "", base.ErrExitStatusMissing
	}
	if *result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("WinRM file operation exited with status %d", *result.ExitCode)
		}
		return "", fmt.Errorf("WinRM file operation: %s", message)
	}
	return strings.TrimSpace(result.Stdout), nil
}

type listedEntry struct {
	Name    string `json:"n"`
	Dir     bool   `json:"d"`
	Size    int64  `json:"l"`
	ModTime string `json:"m"`
}

func parseList(path, output string) ([]base.FileEntry, error) {
	if strings.TrimSpace(output) == "" {
		return []base.FileEntry{}, nil
	}
	var entries []listedEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return nil, fmt.Errorf("parse WinRM directory JSON: %w", err)
	}
	result := make([]base.FileEntry, 0, len(entries))
	for _, entry := range entries {
		kind := base.FileFile
		size := entry.Size
		if entry.Dir {
			kind = base.FileDirectory
			size = 0
		}
		if size < 0 {
			size = 0
		}
		modTime, err := time.Parse(time.RFC3339Nano, entry.ModTime)
		if err != nil {
			return nil, fmt.Errorf("parse WinRM modification time for %s: %w", entry.Name, err)
		}
		fullPath := path + `\` + entry.Name
		if strings.HasSuffix(path, `\`) || strings.HasSuffix(path, "/") {
			fullPath = path + entry.Name
		}
		result = append(result, base.FileEntry{
			Name:    entry.Name,
			Path:    fullPath,
			Kind:    kind,
			Size:    size,
			ModTime: modTime,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if (result[i].Kind == base.FileDirectory) != (result[j].Kind == base.FileDirectory) {
			return result[i].Kind == base.FileDirectory
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (f *FileSystem) List(ctx context.Context, path string) ([]base.FileEntry, error) {
	quoted := quoteLiteral(path)
	output, err := f.execute(ctx, "$ErrorActionPreference='Stop'; $items=@(Get-ChildItem -Force -LiteralPath "+quoted+" | ForEach-Object { [PSCustomObject]@{ n=$_.Name; d=$_.PSIsContainer; l=$_.Length; m=$_.LastWriteTimeUtc.ToString('o') } }); ConvertTo-Json -InputObject $items -Compress")
	if err != nil {
		return nil, err
	}
	return parseList(path, output)
}

func (f *FileSystem) Mkdir(ctx context.Context, path string) error {
	_, err := f.execute(ctx, "$ErrorActionPreference='Stop'; New-Item -ItemType Directory -Force -Path "+quoteLiteral(path)+" | Out-Null")
	return err
}

func (f *FileSystem) Rename(ctx context.Context, from, to string) error {
	_, err := f.execute(ctx, "$ErrorActionPreference='Stop'; Move-Item -LiteralPath "+quoteLiteral(from)+" -Destination "+quoteLiteral(to)+" -Force")
	return err
}

func (f *FileSystem) Delete(ctx context.Context, path string, isDir bool) error {
	command := "Remove-Item -LiteralPath " + quoteLiteral(path) + " -Force"
	if isDir {
		command = "Remove-Item -LiteralPath " + quoteLiteral(path) + " -Recurse -Force"
	}
	_, err := f.execute(ctx, "$ErrorActionPreference='Stop'; "+command)
	return err
}

func (f *FileSystem) Chmod(ctx context.Context, _ string, _ fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: Windows targets do not support chmod", base.ErrUnsupported)
}

func (f *FileSystem) Checksum(ctx context.Context, path, algorithm string) (string, error) {
	var remoteAlgorithm string
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "md5":
		remoteAlgorithm = "MD5"
	case "sha256", "sha-256":
		remoteAlgorithm = "SHA256"
	default:
		return "", fmt.Errorf("unsupported checksum algorithm %q (want md5 or sha256)", algorithm)
	}
	output, err := f.execute(ctx, "$ErrorActionPreference='Stop'; (Get-FileHash -LiteralPath "+quoteLiteral(path)+" -Algorithm "+remoteAlgorithm+").Hash")
	if err != nil {
		return "", err
	}
	return strings.ToLower(output), nil
}

func (f *FileSystem) Exists(ctx context.Context, path string) (bool, error) {
	output, err := f.execute(ctx, "if (Test-Path -LiteralPath "+quoteLiteral(path)+") { 'yes' } else { 'no' }")
	if err != nil {
		return false, err
	}
	switch output {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected WinRM Test-Path response %q", output)
	}
}

func (f *FileSystem) Size(ctx context.Context, path string) (int64, error) {
	output, err := f.execute(ctx, "$ErrorActionPreference='Stop'; (Get-Item -LiteralPath "+quoteLiteral(path)+").Length")
	if err != nil {
		return 0, err
	}
	size, err := strconv.ParseInt(output, 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("parse WinRM file size %q", output)
	}
	return size, nil
}

var _ base.FileSystem = (*FileSystem)(nil)
