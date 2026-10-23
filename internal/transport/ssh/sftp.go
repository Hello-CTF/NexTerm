package ssh

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

const windowsRootsScript = "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ErrorActionPreference='Stop'; try { $windows=[Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT; $supported=$false; $items=@(); $userProfile=''; if ($windows) { $userProfile=[Environment]::GetFolderPath('UserProfile'); $provider=Get-PSProvider -PSProvider FileSystem -ErrorAction SilentlyContinue; if ($null -ne $provider) { $supported=$true; $items=@(Get-PSDrive -PSProvider FileSystem | ForEach-Object { [PSCustomObject]@{n=$_.Name;r=$_.Root} }) } }; $result=[PSCustomObject]@{windows=$windows;supported=$supported;status='ok';kind='';message='';home=$userProfile;roots=$items} } catch { $exception=$_.Exception; while ($null -ne $exception.InnerException) { $exception=$exception.InnerException }; $kind='io'; $message=$exception.Message; if ($exception -is [System.UnauthorizedAccessException] -or $message -match '(?i)access.*denied|permission denied') { $kind='permission' } elseif ($message -match '(?i)device is not ready|no media|device not connected') { $kind='no_media' } elseif ($exception -is [System.IO.DriveNotFoundException] -or $message -match '(?i)does not exist|cannot find drive') { $kind='not_found' }; $result=[PSCustomObject]@{windows=($windows -eq $true);supported=$false;status='error';kind=$kind;message=$message;home='';roots=@()} }; ConvertTo-Json -InputObject $result -Compress -Depth 4"

type sftpState struct {
	client     *sftp.Client
	session    *gossh.Session
	filesystem *sftpFileSystem
}

type sftpRealPath interface {
	RealPath(string) (string, error)
}

type sftpEnvironment struct {
	pathStyle       base.PathStyle
	powerShell      string
	drivePrefix     string
	homePath        string
	mapWindowsPaths bool
}

type sftpFileSystem struct {
	*sshfs.FS
	executor    sshfs.Executor
	realPath    sftpRealPath
	environment sync.RWMutex
	target      *sftpEnvironment
}

type windowsRootsDocument struct {
	Windows   bool                `json:"windows"`
	Supported bool                `json:"supported"`
	Status    string              `json:"status"`
	Kind      string              `json:"kind"`
	Message   string              `json:"message"`
	Home      string              `json:"home"`
	Roots     []windowsListedRoot `json:"roots"`
}

type windowsListedRoot struct {
	Name string `json:"n"`
	Root string `json:"r"`
}

func newSFTPFileSystem(client *sftp.Client, executor sshfs.Executor) *sftpFileSystem {
	return &sftpFileSystem{FS: sshfs.New(client, executor), executor: executor, realPath: client}
}

func (c *Client) FileSystem(ctx context.Context) (base.FileSystem, error) {
	return c.sftpFilesystem(ctx)
}

func (c *Client) SFTP(ctx context.Context) (*sshfs.FS, error) {
	filesystem, err := c.sftpFilesystem(ctx)
	if err != nil {
		return nil, err
	}
	return filesystem.FS, nil
}

func (c *Client) sftpFilesystem(ctx context.Context) (*sftpFileSystem, error) {
	c.sftpMu.Lock()
	defer c.sftpMu.Unlock()
	if c.sftp != nil {
		return c.sftp.filesystem, nil
	}
	if err := c.check(ctx, 0); err != nil {
		return nil, err
	}
	session, err := c.newSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("open SFTP session: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { session.Close() })
	failed := true
	defer func() {
		if failed {
			session.Close()
		}
	}()
	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := session.RequestSubsystem("sftp"); err != nil {
		return nil, fmt.Errorf("request SFTP subsystem: %w", err)
	}
	go io.Copy(io.Discard, stderr)
	client, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil {
		return nil, fmt.Errorf("initialize SFTP: %w", err)
	}
	if !stop() && ctx.Err() != nil {
		client.Close()
		return nil, ctx.Err()
	}
	state := &sftpState{client: client, session: session}
	state.filesystem = newSFTPFileSystem(client, c)
	c.sftp = state
	failed = false
	return state.filesystem, nil
}

func (f *sftpFileSystem) Roots(ctx context.Context) (base.FileSystemRoots, error) {
	environment, document, err := f.targetEnvironment(ctx)
	if err != nil {
		return base.FileSystemRoots{}, err
	}
	if environment.pathStyle == base.PathStylePOSIX {
		return base.FileSystemRoots{
			PathStyle: base.PathStylePOSIX,
			Roots:     []base.FileRoot{{Path: "/", Label: "/"}},
			HomePath:  environment.homePath,
			Supported: true,
		}, nil
	}
	if environment.pathStyle != base.PathStyleWindows {
		return base.FileSystemRoots{}, fmt.Errorf("%w: target filesystem roots are unknown", base.ErrUnsupported)
	}
	if document == nil {
		current, available, err := f.executeWindowsRoots(ctx, environment.powerShell)
		if err != nil {
			return base.FileSystemRoots{}, err
		}
		if !available {
			return base.FileSystemRoots{}, fmt.Errorf("%w: PowerShell is no longer available on the Windows target", base.ErrUnsupported)
		}
		if len(current.Roots) > 0 && !environment.mapWindowsPaths {
			refreshed, err := f.windowsEnvironment(ctx, environment.powerShell, current)
			if err != nil {
				return base.FileSystemRoots{}, err
			}
			f.environment.Lock()
			f.target = refreshed
			f.environment.Unlock()
			environment = refreshed
		}
		document = &current
	}
	return windowsRootsFromDocument(*document)
}

func (f *sftpFileSystem) targetEnvironment(ctx context.Context) (*sftpEnvironment, *windowsRootsDocument, error) {
	f.environment.Lock()
	defer f.environment.Unlock()
	if f.target != nil {
		return f.target, nil, nil
	}
	environment, document, err := f.probeEnvironment(ctx)
	if err != nil {
		return nil, nil, err
	}
	f.target = environment
	return environment, document, nil
}

func (f *sftpFileSystem) probeEnvironment(ctx context.Context) (*sftpEnvironment, *windowsRootsDocument, error) {
	for _, shell := range []string{"powershell.exe", "pwsh"} {
		document, available, err := f.executeWindowsRoots(ctx, shell)
		if err != nil {
			return nil, nil, err
		}
		if !available {
			continue
		}
		if document.Status == "error" {
			return nil, nil, classifyWindowsDocumentError(document.Kind, document.Message)
		}
		if document.Status != "ok" {
			return nil, nil, fmt.Errorf("unexpected Windows roots status %q", document.Status)
		}
		if !document.Windows {
			environment, err := f.posixEnvironment(ctx)
			return environment, nil, err
		}
		if _, err := windowsRootsFromDocument(document); err != nil {
			return nil, nil, err
		}
		environment, err := f.windowsEnvironment(ctx, shell, document)
		return environment, &document, err
	}
	environment, err := f.sftpOnlyEnvironment(ctx)
	return environment, nil, err
}

func (f *sftpFileSystem) executeWindowsRoots(ctx context.Context, shell string) (windowsRootsDocument, bool, error) {
	var document windowsRootsDocument
	result, err := f.executor.Exec(ctx, powerShellCommand(shell, windowsRootsScript), base.ExecOptions{
		Limits: base.OutputLimits{Stdout: -1, Stderr: -1},
	})
	if err != nil {
		if errors.Is(err, base.ErrUnsupported) {
			return document, false, nil
		}
		return document, false, err
	}
	if result.ExitCode == nil {
		return document, false, base.ErrExitStatusMissing
	}
	if windowsCommandNotFound(result, shell) {
		return document, false, nil
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &document); err != nil {
		if *result.ExitCode != 0 {
			return document, true, windowsExecResultError(result)
		}
		return document, true, fmt.Errorf("parse Windows roots JSON: %w", err)
	}
	if *result.ExitCode != 0 {
		return document, true, windowsExecResultError(result)
	}
	return document, true, nil
}

func windowsCommandNotFound(result base.ExecResult, shell string) bool {
	if result.ExitCode == nil || *result.ExitCode != 127 && *result.ExitCode != 9009 {
		return false
	}
	message := strings.ToLower(result.Stdout + "\n" + result.Stderr)
	// POSIX shells report "<command>: ..." and cmd reports "'<command>' ..." in the
	// host's language, so the command name identifies a localized not-found message.
	shell = strings.ToLower(shell)
	for _, fragment := range []string{"not recognized", "not found", "command not found", "no such file or directory", "unknown command", shell + ":", "'" + shell + "'"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func windowsExecResultError(result base.ExecResult) error {
	message := strings.TrimSpace(result.Stderr)
	if message == "" {
		message = strings.TrimSpace(result.Stdout)
	}
	if message == "" && result.ExitCode != nil {
		message = fmt.Sprintf("PowerShell roots query exited with status %d", *result.ExitCode)
	}
	lower := strings.ToLower(message)
	kind := ""
	switch {
	case strings.Contains(lower, "access") && strings.Contains(lower, "denied"), strings.Contains(lower, "permission denied"), strings.Contains(lower, "unauthorizedaccessexception"):
		kind = "permission"
	case strings.Contains(lower, "device is not ready"), strings.Contains(lower, "no media in the drive"), strings.Contains(lower, "device not connected"):
		kind = "no_media"
	case strings.Contains(lower, "does not exist"), strings.Contains(lower, "cannot find drive"):
		kind = "not_found"
	}
	return classifyWindowsDocumentError(kind, message)
}

func powerShellCommand(shell, script string) string {
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(encoded[index*2:], unit)
	}
	return shell + " -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(encoded)
}

func (f *sftpFileSystem) posixEnvironment(ctx context.Context) (*sftpEnvironment, error) {
	home, err := f.realPath.RealPath(".")
	if err != nil {
		return nil, normalizeSFTPError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(home, "/") {
		return nil, fmt.Errorf("%w: SFTP home %q is not a POSIX absolute path", base.ErrUnsupported, home)
	}
	return &sftpEnvironment{pathStyle: base.PathStylePOSIX, homePath: home}, nil
}

func (f *sftpFileSystem) windowsEnvironment(ctx context.Context, shell string, document windowsRootsDocument) (*sftpEnvironment, error) {
	environment := &sftpEnvironment{pathStyle: base.PathStyleWindows, powerShell: shell, homePath: document.Home}
	if len(document.Roots) == 0 {
		return environment, nil
	}
	home, err := f.realPath.RealPath(".")
	if err != nil {
		return nil, normalizeSFTPError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if document.Home != "" && windowsDriveAbsolute(home) {
		for _, prefix := range []string{"", "/"} {
			if sameSFTPPath(windowsPathForSFTP(document.Home, prefix), home) {
				environment.drivePrefix = prefix
				environment.mapWindowsPaths = true
				return environment, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: cannot verify Windows shell paths against SFTP home %q", base.ErrUnsupported, home)
}

func (f *sftpFileSystem) sftpOnlyEnvironment(ctx context.Context) (*sftpEnvironment, error) {
	home, err := f.realPath.RealPath(".")
	if err != nil {
		return nil, normalizeSFTPError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.HasPrefix(home, "/") {
		return &sftpEnvironment{pathStyle: base.PathStylePOSIX, homePath: home}, nil
	}
	if windowsDriveAbsolute(home) || windowsUNCPath(home) {
		return nil, fmt.Errorf("%w: Windows target roots require PowerShell", base.ErrUnsupported)
	}
	return nil, fmt.Errorf("%w: target filesystem path style is unknown", base.ErrUnsupported)
}

func windowsRootsFromDocument(document windowsRootsDocument) (base.FileSystemRoots, error) {
	if document.Status == "error" {
		return base.FileSystemRoots{}, classifyWindowsDocumentError(document.Kind, document.Message)
	}
	if document.Status != "ok" {
		return base.FileSystemRoots{}, fmt.Errorf("unexpected Windows roots status %q", document.Status)
	}
	if !document.Windows || !document.Supported {
		return base.FileSystemRoots{}, fmt.Errorf("%w: Windows FileSystem roots are unavailable", base.ErrUnsupported)
	}
	if document.Home != "" && !windowsShellAbsolutePath(document.Home) {
		return base.FileSystemRoots{}, fmt.Errorf("Windows home path is not absolute: %q", document.Home)
	}
	result := base.FileSystemRoots{
		PathStyle: base.PathStyleWindows,
		Roots:     make([]base.FileRoot, 0, len(document.Roots)),
		HomePath:  document.Home,
		Supported: true,
	}
	for _, root := range document.Roots {
		if !windowsShellAbsolutePath(root.Root) {
			return base.FileSystemRoots{}, fmt.Errorf("Windows root is not an absolute FileSystem path: %q", root.Root)
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

func classifyWindowsDocumentError(kind, message string) error {
	if message == "" {
		message = "Windows roots query failed"
	}
	var cause error
	switch kind {
	case "permission":
		cause = fs.ErrPermission
	case "no_media":
		cause = base.ErrNoMedia
	case "not_found":
		cause = fs.ErrNotExist
	case "unsupported":
		cause = base.ErrUnsupported
	default:
		return fmt.Errorf("Windows roots query: %s", message)
	}
	return fmt.Errorf("Windows roots query: %s: %w", message, cause)
}

func (f *sftpFileSystem) currentEnvironment() *sftpEnvironment {
	f.environment.RLock()
	defer f.environment.RUnlock()
	return f.target
}

func (f *sftpFileSystem) sftpPath(remotePath string) string {
	environment := f.currentEnvironment()
	if environment == nil || environment.pathStyle != base.PathStyleWindows || !environment.mapWindowsPaths {
		return remotePath
	}
	return windowsPathForSFTP(remotePath, environment.drivePrefix)
}

func windowsPathForSFTP(remotePath, drivePrefix string) string {
	if windowsUNCPath(remotePath) {
		return `\\` + strings.ReplaceAll(remotePath[2:], `\`, "/")
	}
	if windowsDriveAbsolute(remotePath) {
		if strings.HasPrefix(remotePath, "/") {
			remotePath = remotePath[1:]
		}
		return drivePrefix + strings.ReplaceAll(remotePath, `\`, "/")
	}
	return remotePath
}

func windowsDriveAbsolute(value string) bool {
	if len(value) >= 4 && value[0] == '/' && windowsDriveLetter(value[1]) && value[2] == ':' && windowsPathSeparator(value[3]) {
		return true
	}
	return len(value) >= 3 && windowsDriveLetter(value[0]) && value[1] == ':' && windowsPathSeparator(value[2])
}

func windowsShellAbsolutePath(value string) bool {
	if len(value) >= 3 && windowsDriveLetter(value[0]) && value[1] == ':' && windowsPathSeparator(value[2]) {
		return true
	}
	return windowsUNCPath(value)
}

func windowsUNCPath(value string) bool {
	if len(value) < 4 || !windowsPathSeparator(value[0]) || !windowsPathSeparator(value[1]) {
		return false
	}
	parts := strings.FieldsFunc(value[2:], func(r rune) bool { return r == '/' || r == '\\' })
	return len(parts) >= 2
}

func windowsDriveLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func windowsPathSeparator(value byte) bool {
	return value == '/' || value == '\\'
}

func sameSFTPPath(left, right string) bool {
	normalize := func(value string) string {
		return strings.TrimRight(strings.ReplaceAll(value, `\`, "/"), "/")
	}
	return strings.EqualFold(normalize(left), normalize(right))
}

func windowsDisplayChild(parent, name string) string {
	if parent == "" {
		return name
	}
	if strings.HasSuffix(parent, "/") || strings.HasSuffix(parent, `\`) {
		return parent + name
	}
	separator := "/"
	if strings.Contains(parent, `\`) && !strings.Contains(parent, "/") {
		separator = `\`
	}
	return parent + separator + name
}

func normalizeSFTPError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrInvalid) ||
		errors.Is(err, base.ErrNoMedia) || errors.Is(err, base.ErrUnsupported) || errors.Is(err, base.ErrDisconnected) ||
		errors.Is(err, base.ErrClosed) || errors.Is(err, base.ErrStaleGeneration) || errors.Is(err, base.ErrExitStatusMissing) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	message := err.Error()
	if index := strings.LastIndex(message, ": "); index >= 0 {
		message = message[index+2:]
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(lower, "device is not ready"), strings.Contains(lower, "no media in the drive"), strings.Contains(lower, "device not connected"):
		return fmt.Errorf("%w: %w", base.ErrNoMedia, err)
	case strings.Contains(lower, "access") && strings.Contains(lower, "denied"), strings.Contains(lower, "permission denied"):
		return fmt.Errorf("%w: %w", fs.ErrPermission, err)
	default:
		return err
	}
}

func (f *sftpFileSystem) List(ctx context.Context, remotePath string) ([]base.FileEntry, error) {
	entries, err := f.FS.List(ctx, f.sftpPath(remotePath))
	if err != nil {
		return nil, normalizeSFTPError(err)
	}
	environment := f.currentEnvironment()
	if environment != nil && environment.pathStyle == base.PathStyleWindows && (windowsShellAbsolutePath(remotePath) || windowsDriveAbsolute(remotePath)) {
		for index := range entries {
			entries[index].Path = windowsDisplayChild(remotePath, entries[index].Name)
		}
	}
	return entries, nil
}

func (f *sftpFileSystem) ReadFile(ctx context.Context, remotePath string, maxBytes int64) ([]byte, error) {
	data, err := f.FS.ReadFile(ctx, f.sftpPath(remotePath), maxBytes)
	return data, normalizeSFTPError(err)
}

func (f *sftpFileSystem) WriteFile(ctx context.Context, remotePath string, data []byte, backup bool) error {
	return normalizeSFTPError(f.FS.WriteFile(ctx, f.sftpPath(remotePath), data, backup))
}

func (f *sftpFileSystem) Mkdir(ctx context.Context, remotePath string) error {
	return normalizeSFTPError(f.FS.Mkdir(ctx, f.sftpPath(remotePath)))
}

func (f *sftpFileSystem) Rename(ctx context.Context, from, to string) error {
	return normalizeSFTPError(f.FS.Rename(ctx, f.sftpPath(from), f.sftpPath(to)))
}

func (f *sftpFileSystem) Delete(ctx context.Context, remotePath string, directory bool) error {
	return normalizeSFTPError(f.FS.Delete(ctx, f.sftpPath(remotePath), directory))
}

func (f *sftpFileSystem) Chmod(ctx context.Context, remotePath string, mode fs.FileMode) error {
	return normalizeSFTPError(f.FS.Chmod(ctx, f.sftpPath(remotePath), mode))
}

func (f *sftpFileSystem) Checksum(ctx context.Context, remotePath, algorithm string) (string, error) {
	digest, err := f.FS.Checksum(ctx, f.sftpPath(remotePath), algorithm)
	return digest, normalizeSFTPError(err)
}

func (f *sftpFileSystem) Exists(ctx context.Context, remotePath string) (bool, error) {
	exists, err := f.FS.Exists(ctx, f.sftpPath(remotePath))
	return exists, normalizeSFTPError(err)
}

func (f *sftpFileSystem) Size(ctx context.Context, remotePath string) (int64, error) {
	size, err := f.FS.Size(ctx, f.sftpPath(remotePath))
	return size, normalizeSFTPError(err)
}

func (f *sftpFileSystem) OpenRead(ctx context.Context, remotePath string) (base.RemoteReader, error) {
	reader, err := f.FS.OpenRead(ctx, f.sftpPath(remotePath))
	return reader, normalizeSFTPError(err)
}

func (f *sftpFileSystem) OpenWrite(ctx context.Context, remotePath string, append bool) (base.RemoteWriter, error) {
	writer, err := f.FS.OpenWrite(ctx, f.sftpPath(remotePath), append)
	return writer, normalizeSFTPError(err)
}

func (f *sftpFileSystem) Upload(ctx context.Context, localPath, remotePath string, options sshfs.TransferOptions) (int64, error) {
	return f.FS.Upload(ctx, localPath, f.sftpPath(remotePath), options)
}

func (f *sftpFileSystem) Download(ctx context.Context, remotePath, localPath string, options sshfs.TransferOptions) (int64, error) {
	return f.FS.Download(ctx, f.sftpPath(remotePath), localPath, options)
}

func (f *sftpFileSystem) ReadRange(ctx context.Context, remotePath string, offset, maxBytes int64) ([]byte, int64, error) {
	data, size, err := f.FS.ReadRange(ctx, f.sftpPath(remotePath), offset, maxBytes)
	return data, size, normalizeSFTPError(err)
}

func (f *sftpFileSystem) WriteFileVersion(ctx context.Context, remotePath string, data []byte, backup bool, expected conditional.Expectation) error {
	return normalizeSFTPError(f.FS.WriteFileVersion(ctx, f.sftpPath(remotePath), data, backup, expected))
}

func (f *sftpFileSystem) PackDownload(ctx context.Context, remotePath, localPath string, options sshfs.TransferOptions) (int64, error) {
	if environment := f.currentEnvironment(); environment != nil && environment.pathStyle == base.PathStyleWindows {
		return 0, fmt.Errorf("%w: SFTP archive operations on Windows targets", base.ErrUnsupported)
	}
	return f.FS.PackDownload(ctx, remotePath, localPath, options)
}

func (f *sftpFileSystem) Extract(ctx context.Context, remotePath string) (string, error) {
	if environment := f.currentEnvironment(); environment != nil && environment.pathStyle == base.PathStyleWindows {
		return "", fmt.Errorf("%w: SFTP archive operations on Windows targets", base.ErrUnsupported)
	}
	return f.FS.Extract(ctx, remotePath)
}

func (c *Client) closeSFTP() {
	c.sftpMu.Lock()
	defer c.sftpMu.Unlock()
	if c.sftp == nil {
		return
	}
	c.sftp.client.Close()
	c.sftp.session.Close()
	c.sftp = nil
}

var (
	_ base.FileSystem    = (*sftpFileSystem)(nil)
	_ base.RootsProvider = (*sftpFileSystem)(nil)
	_ conditional.Writer = (*sftpFileSystem)(nil)
)
