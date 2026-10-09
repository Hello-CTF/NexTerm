package ssh

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestOpenSSHInterop(t *testing.T) {
	if os.Getenv("NEXTERM_SSH_INTEROP") != "1" {
		t.Skip("opt-in OpenSSH/SFTP interoperability skipped: set NEXTERM_SSH_INTEROP=1 plus NEXTERM_SSH_TEST_HOST, USER, HOST_KEY, and PASSWORD or KEY")
	}
	host, portText, err := net.SplitHostPort(os.Getenv("NEXTERM_SSH_TEST_HOST"))
	if err != nil {
		t.Fatalf("NEXTERM_SSH_TEST_HOST must be host:port: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := os.Getenv("NEXTERM_SSH_TEST_HOST_KEY")
	if fingerprint == "" {
		t.Fatal("NEXTERM_SSH_TEST_HOST_KEY is required; tests never disable host-key verification")
	}
	auth := AuthConfig{}
	if password := os.Getenv("NEXTERM_SSH_TEST_PASSWORD"); password != "" {
		auth.Method = AuthPassword
		auth.Password = password
	} else if keyPath := os.Getenv("NEXTERM_SSH_TEST_KEY"); keyPath != "" {
		auth.Method = AuthKey
		auth.KeyPath = keyPath
		auth.Passphrase = os.Getenv("NEXTERM_SSH_TEST_PASSPHRASE")
	} else {
		t.Fatal("set NEXTERM_SSH_TEST_PASSWORD or NEXTERM_SSH_TEST_KEY")
	}
	cfg := Config{
		Host:            host,
		Port:            port,
		User:            os.Getenv("NEXTERM_SSH_TEST_USER"),
		Auth:            auth,
		HostKeys:        NewMemoryHostKeyStore(),
		HostKeyApproval: &HostKeyApproval{Fingerprint: fingerprint},
		ConnectTimeout:  10 * time.Second,
	}
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	remoteRoot := "/tmp/nexterm-go-interop-" + hex.EncodeToString(random[:])
	filesystem, err := client.SFTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Mkdir(context.Background(), remoteRoot); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 15*time.Second)
		defer cancel()
		_, _ = client.Exec(ctx, "rm -rf -- "+sshfs.ShellQuote(remoteRoot), base.ExecOptions{})
	}()

	result, err := client.Exec(context.Background(), "printf interop", base.ExecOptions{Timeout: 15 * time.Second})
	if err != nil || result.Stdout != "interop" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("OpenSSH exec = %+v, %v", result, err)
	}
	remoteFile := remoteRoot + "/file.txt"
	if err := filesystem.WriteFile(context.Background(), remoteFile, []byte("before"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(context.Background(), remoteFile, []byte("after"), true); err != nil {
		t.Fatal(err)
	}
	backup, err := filesystem.ReadFile(context.Background(), remoteFile+".nexterm-bak", 100)
	if err != nil || string(backup) != "before" {
		t.Fatalf("OpenSSH backup = %q, %v", backup, err)
	}
	if err := filesystem.Chmod(context.Background(), remoteFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.Checksum(context.Background(), remoteFile, "sha256"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Rename(context.Background(), remoteFile, remoteRoot+"/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	entries, err := filesystem.List(context.Background(), remoteRoot)
	if err != nil || len(entries) < 2 {
		t.Fatalf("OpenSSH list = %+v, %v", entries, err)
	}

	localDir := t.TempDir()
	source := filepath.Join(localDir, "source.bin")
	if err := os.WriteFile(source, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteUpload := remoteRoot + "/upload.bin"
	if err := filesystem.WriteFile(context.Background(), remoteUpload, []byte("0123"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.Upload(context.Background(), source, remoteUpload, sshfs.TransferOptions{Resume: true}); err != nil {
		t.Fatal(err)
	}
	download := filepath.Join(localDir, "download.bin")
	if err := os.WriteFile(download, []byte("01"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.Download(context.Background(), remoteUpload, download, sshfs.TransferOptions{Resume: true}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(download)
	if string(data) != "0123456789" {
		t.Fatalf("OpenSSH resumed download = %q", data)
	}

	packed := filepath.Join(localDir, "packed.tar.gz")
	if _, err := filesystem.PackDownload(context.Background(), remoteRoot, packed, sshfs.TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	assertSafeTarGz(t, packed)

	archivePath := filepath.Join(localDir, "extract.tar.gz")
	writeTestTarGz(t, archivePath)
	remoteArchive := remoteRoot + "/extract.tar.gz"
	if _, err := filesystem.Upload(context.Background(), archivePath, remoteArchive, sshfs.TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	target, err := filesystem.Extract(context.Background(), remoteArchive)
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := filesystem.ReadFile(context.Background(), path.Join(target, "inside.txt"), 100)
	if err != nil || string(extracted) != "inside" {
		t.Fatalf("OpenSSH extracted file = %q, %v", extracted, err)
	}
}

func assertSafeTarGz(t *testing.T, archivePath string) {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	entries := 0
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries++
		cleaned := path.Clean(header.Name)
		if strings.HasPrefix(header.Name, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			t.Fatalf("unsafe archive member %q", header.Name)
		}
	}
	if entries == 0 {
		t.Fatal("downloaded archive is empty")
	}
}

func writeTestTarGz(t *testing.T, archivePath string) {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte("inside")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "inside.txt", Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
