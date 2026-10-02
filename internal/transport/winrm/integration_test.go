package winrm

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func integrationBool(t *testing.T, name string) bool {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		t.Fatalf("%s must be a boolean: %v", name, err)
	}
	return value
}

func integrationConfig(t *testing.T) Config {
	t.Helper()
	if !integrationBool(t, "NEXTERM_WINRM_INTEGRATION") {
		t.Skip("real Windows Server fixture unavailable: set NEXTERM_WINRM_INTEGRATION=1 and NEXTERM_WINRM_* connection settings")
	}
	config := Config{
		Host:               strings.TrimSpace(os.Getenv("NEXTERM_WINRM_HOST")),
		User:               os.Getenv("NEXTERM_WINRM_USER"),
		Password:           os.Getenv("NEXTERM_WINRM_PASSWORD"),
		Domain:             strings.TrimSpace(os.Getenv("NEXTERM_WINRM_DOMAIN")),
		Auth:               AuthMethod(os.Getenv("NEXTERM_WINRM_AUTH")),
		UseTLS:             integrationBool(t, "NEXTERM_WINRM_TLS"),
		AcceptInvalidCerts: integrationBool(t, "NEXTERM_WINRM_ACCEPT_INVALID_CERTS"),
		TLSServerName:      strings.TrimSpace(os.Getenv("NEXTERM_WINRM_TLS_SERVER_NAME")),
		ProxyURL:           strings.TrimSpace(os.Getenv("NEXTERM_WINRM_PROXY_URL")),
		InitialCWD:         os.Getenv("NEXTERM_WINRM_INITIAL_CWD"),
	}
	if raw := strings.TrimSpace(os.Getenv("NEXTERM_WINRM_PORT")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("NEXTERM_WINRM_PORT must be numeric: %v", err)
		}
		config.Port = port
	}
	if config.Host == "" || strings.TrimSpace(config.User) == "" {
		t.Fatal("opted-in Windows fixture requires NEXTERM_WINRM_HOST and NEXTERM_WINRM_USER")
	}
	if caFile := strings.TrimSpace(os.Getenv("NEXTERM_WINRM_CA_FILE")); caFile != "" {
		ca, err := os.ReadFile(caFile)
		if err != nil {
			t.Fatalf("read NEXTERM_WINRM_CA_FILE: %v", err)
		}
		config.CACert = ca
	}
	return config
}

func realExec(t *testing.T, ctx context.Context, transport *Transport, script string) base.ExecResult {
	t.Helper()
	result, err := transport.Exec(ctx, script, base.ExecOptions{Timeout: 45 * time.Second})
	if err != nil {
		t.Fatalf("real WinRM exec failed: %v (stdout=%q stderr=%q)", err, result.Stdout, result.Stderr)
	}
	return result
}

func requireRealSuccess(t *testing.T, result base.ExecResult) {
	t.Helper()
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("remote command failed: %+v", result)
	}
}

func TestRealWindowsServer(t *testing.T) {
	config := integrationConfig(t)
	transport, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	t.Run("authentication and separate stdout stderr", func(t *testing.T) {
		result := realExec(t, ctx, transport, "[Console]::Out.Write('标准输出'); [Console]::Error.Write('错误输出')")
		requireRealSuccess(t, result)
		if result.Stdout != "标准输出" || !strings.Contains(result.Stderr, "错误输出") {
			t.Fatalf("unexpected stdout/stderr: %+v", result)
		}
	})

	t.Run("domain user", func(t *testing.T) {
		if config.Domain == "" && !strings.Contains(config.User, `\`) && !strings.Contains(config.User, "@") {
			t.Skip("domain-user fixture unavailable: set NEXTERM_WINRM_DOMAIN or use a qualified domain user")
		}
		result := realExec(t, ctx, transport, "whoami")
		requireRealSuccess(t, result)
	})

	t.Run("TLS", func(t *testing.T) {
		if !config.UseTLS && config.Port != 5986 {
			t.Skip("TLS fixture unavailable: rerun with NEXTERM_WINRM_TLS=true or NEXTERM_WINRM_PORT=5986")
		}
		result := realExec(t, ctx, transport, "Write-Output 'tls-ok'")
		requireRealSuccess(t, result)
	})

	t.Run("explicit proxy", func(t *testing.T) {
		if config.ProxyURL == "" {
			t.Skip("WinRM proxy fixture unavailable: set NEXTERM_WINRM_PROXY_URL to an HTTP, HTTPS, or SOCKS5 proxy")
		}
		result := realExec(t, ctx, transport, "Write-Output 'proxy-ok'")
		requireRealSuccess(t, result)
	})

	t.Run("cwd persistence", func(t *testing.T) {
		first := realExec(t, ctx, transport, "Set-Location -LiteralPath $env:TEMP; Write-Output (Get-Location).Path")
		requireRealSuccess(t, first)
		second := realExec(t, ctx, transport, "Write-Output (Get-Location).Path")
		requireRealSuccess(t, second)
		if !strings.EqualFold(strings.TrimRight(first.Stdout, `\`), strings.TrimRight(second.Stdout, `\`)) {
			t.Fatalf("cwd was not retained: first=%q second=%q", first.Stdout, second.Stdout)
		}
	})

	t.Run("GB18030", func(t *testing.T) {
		result := realExec(t, ctx, transport, "[Console]::OutputEncoding=[Text.Encoding]::GetEncoding('GB18030'); [Console]::Out.Write('中文目录𠀀'); [Console]::Error.Write('错误输出')")
		requireRealSuccess(t, result)
		if result.Stdout != "中文目录𠀀" || !strings.Contains(result.Stderr, "错误输出") {
			t.Fatalf("GB18030 decode failed: %+v", result)
		}
	})

	t.Run("native exit status", func(t *testing.T) {
		result := realExec(t, ctx, transport, "& cmd.exe /c exit 23")
		if result.ExitCode == nil || *result.ExitCode != 23 {
			t.Fatalf("native exit status was not preserved: %+v", result)
		}
	})

	t.Run("filesystem", func(t *testing.T) {
		temp := realExec(t, ctx, transport, "Write-Output $env:TEMP")
		requireRealSuccess(t, temp)
		root := fmt.Sprintf(`%s\NexTerm-WinRM-'-%d-%d`, strings.TrimRight(temp.Stdout, `\`), os.Getpid(), time.Now().UnixNano())
		filesystem, err := transport.FileSystem(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cleanupCancel()
			_ = filesystem.Delete(cleanupCtx, root, true)
		})
		if err := filesystem.Mkdir(ctx, root); err != nil {
			t.Fatal(err)
		}
		path := root + `\file's.txt`
		backupPath := path + ".nexterm-bak"
		if err := filesystem.WriteFile(ctx, path, []byte("hello"), false); err != nil {
			t.Fatal(err)
		}
		if err := filesystem.WriteFile(ctx, path, []byte("hello world"), true); err != nil {
			t.Fatal(err)
		}
		content, err := filesystem.ReadFile(ctx, path, 1024)
		if err != nil || string(content) != "hello world" {
			t.Fatalf("ReadFile = %q, %v", content, err)
		}
		backup, err := filesystem.ReadFile(ctx, backupPath, 1024)
		if err != nil || string(backup) != "hello" {
			t.Fatalf("backup = %q, %v", backup, err)
		}
		if _, err := filesystem.ReadFile(ctx, backupPath, 4); err == nil {
			t.Fatal("remote read limit was not enforced")
		}
		entries, err := filesystem.List(ctx, root)
		if err != nil || len(entries) != 2 {
			t.Fatalf("List = %+v, %v", entries, err)
		}
		size, err := filesystem.Size(ctx, path)
		if err != nil || size != int64(len("hello world")) {
			t.Fatalf("Size = %d, %v", size, err)
		}
		sha, err := filesystem.Checksum(ctx, path, "sha256")
		if err != nil {
			t.Fatal(err)
		}
		expectedSHA := sha256.Sum256([]byte("hello world"))
		if sha != hex.EncodeToString(expectedSHA[:]) {
			t.Fatalf("SHA256 = %q", sha)
		}
		md5sum, err := filesystem.Checksum(ctx, path, "md5")
		if err != nil {
			t.Fatal(err)
		}
		expectedMD5 := md5.Sum([]byte("hello world"))
		if md5sum != hex.EncodeToString(expectedMD5[:]) {
			t.Fatalf("MD5 = %q", md5sum)
		}
		renamed := root + `\renamed.txt`
		if err := filesystem.Rename(ctx, path, renamed); err != nil {
			t.Fatal(err)
		}
		if err := filesystem.Delete(ctx, renamed, false); err != nil {
			t.Fatal(err)
		}
		exists, err := filesystem.Exists(ctx, renamed)
		if err != nil || exists {
			t.Fatalf("deleted file Exists = %v, %v", exists, err)
		}
		if err := filesystem.Delete(ctx, root, true); err != nil {
			t.Fatal(err)
		}
		exists, err = filesystem.Exists(ctx, root)
		if err != nil || exists {
			t.Fatalf("recursive delete Exists = %v, %v", exists, err)
		}
		if err := filesystem.WriteFile(ctx, root+`\too-large`, make([]byte, 48*1024+1), false); err == nil {
			t.Fatal("oversized WinRM write succeeded")
		}
		if err := filesystem.Chmod(ctx, root, fs.FileMode(0o755)); !errors.Is(err, base.ErrUnsupported) {
			t.Fatalf("Chmod = %v", err)
		}
		if _, err := filesystem.OpenRead(ctx, path); !errors.Is(err, base.ErrUnsupported) {
			t.Fatalf("OpenRead = %v", err)
		}
		if _, err := filesystem.OpenWrite(ctx, path, false); !errors.Is(err, base.ErrUnsupported) {
			t.Fatalf("OpenWrite = %v", err)
		}
	})

	t.Run("relative read write and backup after cwd change", func(t *testing.T) {
		temp := realExec(t, ctx, transport, "Write-Output $env:TEMP")
		requireRealSuccess(t, temp)
		processCWD := realExec(t, ctx, transport, "[Console]::Out.Write([Environment]::CurrentDirectory)")
		requireRealSuccess(t, processCWD)
		unique := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
		root := fmt.Sprintf(`%s\NexTerm-WinRM-relative-%s`, strings.TrimRight(temp.Stdout, `\`), unique)
		name := "relative-'" + unique + ".txt"
		decoy := fmt.Sprintf(`%s\%s`, strings.TrimRight(processCWD.Stdout, `\`), name)
		filesystem, err := transport.FileSystem(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cleanupCancel()
			_ = filesystem.Delete(cleanupCtx, root, true)
			_ = filesystem.Delete(cleanupCtx, decoy, false)
		})
		if err := filesystem.Mkdir(ctx, root); err != nil {
			t.Fatal(err)
		}
		changed := realExec(t, ctx, transport, "Set-Location -LiteralPath "+quoteLiteral(root)+"; Write-Output (Get-Location).Path")
		requireRealSuccess(t, changed)
		if err := filesystem.WriteFile(ctx, decoy, []byte("process-cwd-decoy"), false); err != nil {
			t.Skipf("process-cwd collision fixture unavailable; the remote process directory is not writable: %v", err)
		}
		if err := filesystem.WriteFile(ctx, name, []byte("powershell-cwd-original"), false); err != nil {
			t.Fatal(err)
		}
		if err := filesystem.WriteFile(ctx, name, []byte("powershell-cwd-replacement"), true); err != nil {
			t.Fatal(err)
		}
		content, err := filesystem.ReadFile(ctx, name, 1024)
		if err != nil || string(content) != "powershell-cwd-replacement" {
			t.Fatalf("relative ReadFile after cwd change = %q, %v", content, err)
		}
		backup, err := filesystem.ReadFile(ctx, name+".nexterm-bak", 1024)
		if err != nil || string(backup) != "powershell-cwd-original" {
			t.Fatalf("relative backup after cwd change = %q, %v", backup, err)
		}
		decoyContent, err := filesystem.ReadFile(ctx, decoy, 1024)
		if err != nil || string(decoyContent) != "process-cwd-decoy" {
			t.Fatalf("relative I/O touched the .NET process-cwd file: %q, %v", decoyContent, err)
		}
	})
}
