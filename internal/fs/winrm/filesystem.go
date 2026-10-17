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

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const BackupSuffix = ".nexterm-bak"

const rootsScript = "$ErrorActionPreference='Stop'; try { $windows=[Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT; $supported=$false; $items=@(); $__nextermHome=''; if ($windows) { $__nextermHome=[Environment]::GetFolderPath('UserProfile'); $provider=Get-PSProvider -PSProvider FileSystem -ErrorAction SilentlyContinue; if ($null -ne $provider) { $supported=$true; $items=@(Get-PSDrive -PSProvider FileSystem | ForEach-Object { [PSCustomObject]@{n=$_.Name;r=$_.Root} }) } }; $result=[PSCustomObject]@{windows=$windows;supported=$supported;status='ok';kind='';message='';home=$__nextermHome;roots=$items} } catch { $exception=$_.Exception; while ($null -ne $exception.InnerException) { $exception=$exception.InnerException }; $kind='io'; $message=$exception.Message; if ($exception -is [System.UnauthorizedAccessException] -or $message -match '(?i)access.*denied|permission denied') { $kind='permission' } elseif ($message -match '(?i)device is not ready|no media|device not connected') { $kind='no_media' } elseif ($exception -is [System.IO.DriveNotFoundException] -or $message -match '(?i)does not exist|cannot find drive') { $kind='not_found' }; $result=[PSCustomObject]@{windows=($windows -eq $true);supported=$false;status='error';kind=$kind;message=$message;home='';roots=@()} }; ConvertTo-Json -InputObject $result -Compress -Depth 4"

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
	script = "try { " + script + "\n} catch { $__nextermException=$_.Exception; while ($null -ne $__nextermException.InnerException) { $__nextermException=$__nextermException.InnerException }; $__nextermCode=$__nextermException.HResult -band 0xFFFF; $__nextermKind='io'; if ($__nextermException -is [System.UnauthorizedAccessException] -or $__nextermCode -eq 5) { $__nextermKind='permission' } elseif ($__nextermCode -eq 21 -or $__nextermCode -eq 1112 -or $__nextermCode -eq 1167) { $__nextermKind='no_media' } elseif ($__nextermException -is [System.IO.DriveNotFoundException] -or $__nextermCode -eq 2 -or $__nextermCode -eq 3 -or $__nextermCode -eq 15 -or $__nextermCode -eq 20) { $__nextermKind='not_found' }; [Console]::Error.WriteLine('<<NEXTERM_FS_ERROR>>'+$__nextermKind+':'+$__nextermException.Message); exit 1 }"
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
		kind := ""
		message := strings.TrimSpace(result.Stderr)
		if index := strings.LastIndex(message, "<<NEXTERM_FS_ERROR>>"); index >= 0 {
			if structuredKind, structuredMessage, ok := strings.Cut(message[index+len("<<NEXTERM_FS_ERROR>>"):], ":"); ok {
				kind = structuredKind
				message = strings.TrimSpace(structuredMessage)
			}
		}
		if message == "" {
			message = fmt.Sprintf("WinRM file operation exited with status %d", *result.ExitCode)
		}
		return "", classifyWinRMError(kind, message)
	}
	return strings.TrimSpace(result.Stdout), nil
}

type rootsDocument struct {
	Windows   bool         `json:"windows"`
	Supported bool         `json:"supported"`
	Status    string       `json:"status"`
	Kind      string       `json:"kind"`
	Message   string       `json:"message"`
	Home      string       `json:"home"`
	Roots     []listedRoot `json:"roots"`
}

type listedRoot struct {
	Name string `json:"n"`
	Root string `json:"r"`
}

func (f *FileSystem) Roots(ctx context.Context) (base.FileSystemRoots, error) {
	output, err := f.execute(ctx, rootsScript)
	if err != nil {
		return base.FileSystemRoots{}, err
	}
	var document rootsDocument
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		return base.FileSystemRoots{}, fmt.Errorf("parse WinRM roots JSON: %w", err)
	}
	if document.Status == "error" {
		return base.FileSystemRoots{}, classifyWinRMError(document.Kind, document.Message)
	}
	if document.Status != "ok" {
		return base.FileSystemRoots{}, fmt.Errorf("unexpected WinRM roots status %q", document.Status)
	}
	if !document.Windows || !document.Supported {
		return base.FileSystemRoots{}, fmt.Errorf("%w: Windows FileSystem roots are unavailable", base.ErrUnsupported)
	}
	if document.Home != "" && !windowsAbsolutePath(document.Home) {
		return base.FileSystemRoots{}, fmt.Errorf("WinRM home path is not absolute: %q", document.Home)
	}
	result := base.FileSystemRoots{
		PathStyle: base.PathStyleWindows,
		Roots:     make([]base.FileRoot, 0, len(document.Roots)),
		HomePath:  document.Home,
		Supported: true,
	}
	for _, root := range document.Roots {
		if !windowsAbsolutePath(root.Root) {
			return base.FileSystemRoots{}, fmt.Errorf("WinRM root is not an absolute FileSystem path: %q", root.Root)
		}
		label := root.Name
		if len(label) == 1 {
			label += ":"
		}
		if label == "" {
			label = root.Root
		}
		result.Roots = append(result.Roots, base.FileRoot{Path: root.Root, Label: label})
	}
	sort.Slice(result.Roots, func(i, j int) bool {
		return strings.ToUpper(result.Roots[i].Path) < strings.ToUpper(result.Roots[j].Path)
	})
	return result, nil
}

func windowsAbsolutePath(value string) bool {
	if len(value) >= 3 && isDriveLetter(value[0]) && value[1] == ':' && windowsPathSeparator(value[2]) {
		return true
	}
	if len(value) < 4 || !windowsPathSeparator(value[0]) || !windowsPathSeparator(value[1]) {
		return false
	}
	parts := strings.FieldsFunc(value[2:], func(r rune) bool { return r == '/' || r == '\\' })
	return len(parts) >= 2
}

func isDriveLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func windowsPathSeparator(value byte) bool {
	return value == '/' || value == '\\'
}

func classifyWinRMError(kind, message string) error {
	if message == "" {
		message = "WinRM file operation failed"
	}
	lower := strings.ToLower(message)
	if kind == "" {
		switch {
		case strings.Contains(lower, "device is not ready"), strings.Contains(lower, "no media"), strings.Contains(lower, "device not connected"):
			kind = "no_media"
		case strings.Contains(lower, "access") && strings.Contains(lower, "denied"), strings.Contains(lower, "permission denied"), strings.Contains(lower, "unauthorizedaccessexception"):
			kind = "permission"
		case strings.Contains(lower, "does not exist"), strings.Contains(lower, "cannot find drive"), strings.Contains(lower, "cannot find path"):
			kind = "not_found"
		}
	}
	var cause error
	switch kind {
	case "no_media":
		cause = base.ErrNoMedia
	case "permission":
		cause = fs.ErrPermission
	case "not_found":
		cause = fs.ErrNotExist
	case "unsupported":
		cause = base.ErrUnsupported
	default:
		return fmt.Errorf("WinRM file operation: %s", message)
	}
	return fmt.Errorf("WinRM file operation: %s: %w", message, cause)
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

var (
	_ base.FileSystem    = (*FileSystem)(nil)
	_ base.RootsProvider = (*FileSystem)(nil)
)
