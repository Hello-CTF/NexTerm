package winrm

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const MaxWriteBytes = 48 * 1024

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func resolvePath(path string) string {
	return "$__nextermProvider=$null; $__nextermDrive=$null; $__nextermPath=$ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath(" + quoteLiteral(path) + ", [ref]$__nextermProvider, [ref]$__nextermDrive); if ($__nextermProvider.Name -ne 'FileSystem') { throw 'Path must use the FileSystem provider' }; "
}

func (f *FileSystem) ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("read limit must not be negative")
	}
	script := "$ErrorActionPreference='Stop'; " + resolvePath(path) + fmt.Sprintf("$f=Get-Item -LiteralPath $__nextermPath; if ($f.PSIsContainer) { throw 'Path is a directory' }; if ($f.Length -gt %d) { throw 'File exceeds read limit' }; [Convert]::ToBase64String([IO.File]::ReadAllBytes($f.FullName))", maxBytes)
	output, err := f.execute(ctx, script)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(output)
	if err != nil {
		return nil, fmt.Errorf("decode WinRM file content: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds read limit (%d > %d bytes)", len(data), maxBytes)
	}
	return data, nil
}

func (f *FileSystem) WriteFile(ctx context.Context, path string, data []byte, backup bool) error {
	if len(data) > MaxWriteBytes {
		return fmt.Errorf("WinRM write limit is %d bytes, got %d", MaxWriteBytes, len(data))
	}
	backupClause := ""
	if backup {
		backupClause = "if (Test-Path -LiteralPath $__nextermPath) { $__nextermBackup=$__nextermPath+'" + BackupSuffix + "'; Copy-Item -LiteralPath $__nextermPath -Destination $__nextermBackup -Force }; "
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	script := "$ErrorActionPreference='Stop'; " + resolvePath(path) + backupClause + "[IO.File]::WriteAllBytes($__nextermPath, [Convert]::FromBase64String('" + encoded + "'))"
	_, err := f.execute(ctx, script)
	return err
}

func (f *FileSystem) OpenRead(ctx context.Context, _ string) (base.RemoteReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: WinRM streaming reads are unavailable; use ReadFile", base.ErrUnsupported)
}

func (f *FileSystem) OpenWrite(ctx context.Context, _ string, _ bool) (base.RemoteWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: WinRM streaming writes are unavailable; use WriteFile with at most %d bytes", base.ErrUnsupported, MaxWriteBytes)
}
