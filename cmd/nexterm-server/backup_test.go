package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/db/sqlitedriver"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

func runBackupForTest(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runBackup(context.Background(), args, func(key string) string { return env[key] }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestBackupCommandSnapshotsLiveStore(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	live, err := store.Open(ctx, filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close() }()
	if err := live.GORM().Exec("CREATE TABLE backup_probe (id INTEGER PRIMARY KEY, pair INTEGER NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	writerErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for pair := 0; ; pair++ {
			select {
			case <-stop:
				writerErr <- nil
				return
			default:
			}
			if err := live.GORM().Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("INSERT INTO backup_probe (pair) VALUES (?)", pair).Error; err != nil {
					return err
				}
				return tx.Exec("INSERT INTO backup_probe (pair) VALUES (?)", pair).Error
			}); err != nil {
				writerErr <- err
				return
			}
		}
	}()

	outDir := t.TempDir()
	out := filepath.Join(outDir, "nexterm.db")
	code, stdout, stderr := runBackupForTest(t, map[string]string{"NEXTERM_DATA_DIR": dataDir}, "--out", out)
	close(stop)
	wg.Wait()
	if err := <-writerErr; err != nil {
		t.Fatalf("writer: %v", err)
	}
	if code != 0 {
		t.Fatalf("backup exit %d: %s", code, stderr)
	}
	for _, want := range []string{"master.key", "totp.key", "NOT included"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout must mention %q: %s", want, stdout)
		}
	}

	uriPath := filepath.ToSlash(out)
	if runtime.GOOS == "windows" {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open(sqlitedriver.Name, (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
	var rows int64
	if err := db.QueryRow("SELECT COUNT(*) FROM backup_probe").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows%2 != 0 {
		t.Fatalf("backup holds %d rows: a transaction was split", rows)
	}

	data, err := os.ReadFile(out + ".manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if manifest.Format != backupManifestFormat || manifest.Backend != "sqlite" || manifest.SHA256 != hex.EncodeToString(sum[:]) ||
		manifest.SizeBytes != int64(len(content)) || manifest.NexTermVersion == "" || manifest.SchemaFingerprint == "" || manifest.CreatedAt.IsZero() {
		t.Fatalf("manifest = %+v", manifest)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{out, out + ".manifest.json"} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %v, want 0600", path, info.Mode().Perm())
			}
		}
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("output directory must hold only the snapshot and manifest, got %v", entries)
	}
}

func TestBackupCommandRefusesExistingOutputWithoutForce(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	live, err := store.Open(ctx, filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"NEXTERM_DATA_DIR": dataDir}
	out := filepath.Join(t.TempDir(), "nexterm.db")
	if err := os.WriteFile(out, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runBackupForTest(t, env, "--out", out)
	if code != 2 || !strings.Contains(stderr, "--force") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if data, _ := os.ReadFile(out); string(data) != "previous" {
		t.Fatalf("existing output changed without --force: %q", data)
	}
	if _, err := os.Stat(out + ".manifest.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused backup must not write a manifest (err=%v)", err)
	}

	code, _, stderr = runBackupForTest(t, env, "--out="+out, "--force")
	if code != 0 {
		t.Fatalf("forced backup exit %d: %s", code, stderr)
	}
	if data, _ := os.ReadFile(out); !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		t.Fatal("--force must replace the output with the snapshot")
	}
	code, _, stderr = runBackupForTest(t, env, "--out", out, "--force")
	if code != 0 {
		t.Fatalf("second forced backup exit %d: %s", code, stderr)
	}
}

func TestBackupCommandRejectsMissingDatabaseAndLiveTarget(t *testing.T) {
	dataDir := t.TempDir()
	env := map[string]string{"NEXTERM_DATA_DIR": dataDir}
	out := filepath.Join(t.TempDir(), "nexterm.db")
	code, _, stderr := runBackupForTest(t, env, "--out", out)
	if code != 1 || !strings.Contains(stderr, "no SQLite database") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	for _, path := range []string{out, filepath.Join(dataDir, "data.db")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must not be created (err=%v)", path, err)
		}
	}
	if code, _, _ := runBackupForTest(t, env); code != 2 {
		t.Fatalf("missing --out exit %d, want 2", code)
	}
	if code, _, _ := runBackupForTest(t, env, "--out", filepath.Join(dataDir, "data.db"), "--force"); code != 2 {
		t.Fatalf("live database as --out exit %d, want 2", code)
	}
	if code, _, _ := runBackupForTest(t, env, "--out", filepath.Join(dataDir, "data.db-wal"), "--force"); code != 2 {
		t.Fatalf("WAL sidecar as --out exit %d, want 2", code)
	}
}

func TestBackupCommandPrintsRemoteDumpCommandWithoutPassword(t *testing.T) {
	cases := []struct {
		db, dsn string
		want    []string
	}{
		{"postgres", "postgres://nexterm:s3cret-pw@db.example:5433/nexterm_prod?sslmode=verify-full", []string{"pg_dump", "--host=db.example", "--port=5433", "--username=nexterm", "--dbname=nexterm_prod"}},
		{"mysql", "nexterm:s3cret-pw@tcp(db.example:3307)/nexterm_prod?parseTime=true", []string{"mysqldump", "--host=db.example", "--port=3307", "--user=nexterm", "nexterm_prod"}},
		{"mariadb", "nexterm:s3cret-pw@tcp(db.example:3306)/nexterm_prod", []string{"mariadb-dump", "--host=db.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.db, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "dump")
			code, stdout, stderr := runBackupForTest(t, map[string]string{"NEXTERM_DB": tc.db, "NEXTERM_DB_DSN": tc.dsn}, "--out", out)
			if code != backupExitUnsupported {
				t.Fatalf("exit %d, want %d", code, backupExitUnsupported)
			}
			for _, want := range tc.want {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr must contain %q: %s", want, stderr)
				}
			}
			if strings.Contains(stdout+stderr, "s3cret-pw") {
				t.Fatalf("output leaks the password: %s", stderr)
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("remote backend must not write %s (err=%v)", out, err)
			}
		})
	}
}

func TestBackupCommandHelp(t *testing.T) {
	code, stdout, _ := runBackupForTest(t, nil, "--help")
	if code != 0 || !strings.Contains(stdout, "nexterm-server backup --out FILE") {
		t.Fatalf("exit %d, stdout %s", code, stdout)
	}
	if code, _, _ := runBackupForTest(t, nil, "--bogus"); code != 2 {
		t.Fatalf("unknown flag exit %d, want 2", code)
	}
}
