package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/applog"
	"github.com/Hello-CTF/NexTerm/internal/platform"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/version"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

// backupExitUnsupported reports a remote database backend: this command does
// not dump it, and prints the native dump command to run instead.
const backupExitUnsupported = 3

const backupManifestFormat = "nexterm-server-backup"

func backupUsage() string {
	return `Usage:
  nexterm-server backup --out FILE [flags]

Write a consistent snapshot of the SQLite database (<data-dir>/data.db) to
FILE with VACUUM INTO. It is safe while the server is running: the snapshot
holds every transaction committed before it started. FILE is written
atomically with mode 0600, and FILE.manifest.json records the NexTerm
version, schema fingerprint, time, size and SHA-256 of the snapshot.

Run it as the service user (or root), for example:
  sudo -u nexterm /opt/nexterm/nexterm-server backup \
    --data-dir /var/lib/nexterm --out /var/backups/nexterm/data.db

The snapshot does NOT contain the vault master key (master.key, or the file
named by NEXTERM_MASTER_KEY_FILE) or the TOTP key (<data-dir>/totp.key).
Without them the saved credentials and two-factor secrets cannot be
decrypted; back them up separately and keep them away from the database
copy. Other data-directory files (files/, known_hosts.json, ...) are not
included either.

PostgreSQL, MySQL and MariaDB are not dumped by this command: it prints the
pg_dump / mysqldump command for the configured database and exits 3.

Restore (SQLite): stop the service, move data.db, data.db-wal and
data.db-shm out of the data directory, copy the snapshot in as data.db
owned by the service user with mode 0600, then start the service with a
NexTerm version at least as new as the one in the manifest.

Exit codes:
  0  snapshot written
  1  snapshot failed; FILE is unchanged unless the error says it was written
  2  usage error, or FILE exists without --force
  3  remote database backend; dump it with the printed command

Flags:
  --out FILE          Snapshot path (required)
  --force             Replace an existing FILE and its manifest
  --data-dir PATH     Data directory (env NEXTERM_DATA_DIR)
  --db BACKEND        sqlite, postgres, mysql, or mariadb (env NEXTERM_DB, default sqlite)
  --db-dsn DSN        Remote database connection string (env NEXTERM_DB_DSN);
                      only used to print the dump command, never connected to
  --master-key-file PATH  Vault master key location to name in the reminder
                      (env NEXTERM_MASTER_KEY_FILE)
  -h, --help          Print this help
`
}

type backupOptions struct {
	out           string
	force         bool
	dataDir       string
	db            string
	dbDSN         string
	masterKeyFile string
	help          bool
}

func parseBackupArgs(args []string, getenv func(string) string) (backupOptions, error) {
	options := backupOptions{
		dataDir:       getenv("NEXTERM_DATA_DIR"),
		db:            getenv("NEXTERM_DB"),
		dbDSN:         getenv("NEXTERM_DB_DSN"),
		masterKeyFile: getenv("NEXTERM_MASTER_KEY_FILE"),
	}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch argument {
		case "-h", "--help":
			options.help = true
			continue
		case "--force":
			options.force = true
			continue
		}
		if !strings.HasPrefix(argument, "--") {
			return backupOptions{}, fmt.Errorf("unknown argument %q", argument)
		}
		name, value, hasValue := strings.Cut(argument, "=")
		if !hasValue {
			index++
			if index >= len(args) {
				return backupOptions{}, fmt.Errorf("option %s requires a value", name)
			}
			value = args[index]
		}
		switch name {
		case "--out":
			options.out = value
		case "--data-dir":
			options.dataDir = value
		case "--db":
			options.db = value
		case "--db-dsn":
			options.dbDSN = value
		case "--master-key-file":
			options.masterKeyFile = value
		default:
			return backupOptions{}, fmt.Errorf("unknown option %s", name)
		}
	}
	return options, nil
}

type backupManifest struct {
	Format            string    `json:"format"`
	FormatVersion     int       `json:"format_version"`
	Backend           string    `json:"backend"`
	NexTermVersion    string    `json:"nexterm_version"`
	CreatedAt         time.Time `json:"created_at"`
	Source            string    `json:"source"`
	DatabaseFile      string    `json:"database_file"`
	SizeBytes         int64     `json:"size_bytes"`
	SHA256            string    `json:"sha256"`
	SQLiteVersion     string    `json:"sqlite_version"`
	SchemaFingerprint string    `json:"schema_fingerprint"`
	NotIncluded       []string  `json:"not_included"`
}

func runBackup(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	options, err := parseBackupArgs(args, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server backup:", applog.RedactText(err.Error()))
		return 2
	}
	if options.help {
		fmt.Fprint(stdout, backupUsage())
		return 0
	}
	backend, err := store.ParseBackend(options.db)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server backup:", applog.RedactText(err.Error()))
		return 2
	}
	if backend != store.BackendSQLite {
		printRemoteDumpGuidance(stderr, backend, options)
		return backupExitUnsupported
	}
	if options.out == "" {
		fmt.Fprintln(stderr, "nexterm-server backup: --out FILE is required")
		return 2
	}
	paths, err := platform.ServerPaths(options.dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server backup:", applog.RedactText(err.Error()))
		return 1
	}
	out, err := filepath.Abs(options.out)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server backup:", applog.RedactText(err.Error()))
		return 2
	}
	source, err := filepath.Abs(paths.DatabaseFile)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server backup:", applog.RedactText(err.Error()))
		return 1
	}
	sidecar := realDir(out) == realDir(source) && strings.HasPrefix(filepath.Base(out), filepath.Base(source)+"-")
	if sameFile(source, out) || sidecar {
		fmt.Fprintln(stderr, "nexterm-server backup: --out must not be the live database or one of its sidecar files")
		return 2
	}
	manifestPath := out + ".manifest.json"
	if !options.force {
		for _, target := range []string{out, manifestPath} {
			if _, err := os.Lstat(target); err == nil {
				fmt.Fprintf(stderr, "nexterm-server backup: %s already exists; pass --force to replace it\n", target)
				return 2
			}
		}
	}
	if _, err := os.Stat(source); err != nil {
		fmt.Fprintf(stderr, "nexterm-server backup: no SQLite database at %s (pass --data-dir, or --db for a remote backend): %s\n", source, applog.RedactText(err.Error()))
		return 1
	}
	manifest, err := writeSQLiteBackup(ctx, source, out, manifestPath, options.force)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server backup:", applog.RedactText(err.Error()))
		return 1
	}
	fmt.Fprintf(stdout, "nexterm-server backup: wrote %s (%d bytes, sha256 %s)\n", out, manifest.SizeBytes, manifest.SHA256)
	fmt.Fprintf(stdout, "nexterm-server backup: manifest %s\n", manifestPath)
	printKeyReminder(stdout, paths.DataDir, options.masterKeyFile)
	return 0
}

// writeSQLiteBackup snapshots source into a private directory beside out and
// renames it into place, so out is either the previous file or a complete,
// verified snapshot.
func writeSQLiteBackup(ctx context.Context, source, out, manifestPath string, force bool) (backupManifest, error) {
	work, err := os.MkdirTemp(filepath.Dir(out), ".nexterm-backup-")
	if err != nil {
		return backupManifest{}, fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	staged := filepath.Join(work, "data.db")
	snapshot, err := store.SnapshotSQLite(ctx, source, staged)
	if err != nil {
		return backupManifest{}, err
	}
	if err := os.Chmod(staged, 0o600); err != nil {
		return backupManifest{}, err
	}
	size, sum, err := syncAndHash(staged)
	if err != nil {
		return backupManifest{}, err
	}
	manifest := backupManifest{
		Format:            backupManifestFormat,
		FormatVersion:     1,
		Backend:           string(store.BackendSQLite),
		NexTermVersion:    version.Version,
		CreatedAt:         time.Now().UTC().Truncate(time.Second),
		Source:            source,
		DatabaseFile:      filepath.Base(out),
		SizeBytes:         size,
		SHA256:            sum,
		SQLiteVersion:     snapshot.SQLiteVersion,
		SchemaFingerprint: snapshot.SchemaFingerprint,
		NotIncluded:       []string{"master.key", "totp.key", "other data-directory files"},
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return backupManifest{}, err
	}
	stagedManifest := filepath.Join(work, "manifest.json")
	if err := writeSyncedFile(stagedManifest, append(encoded, '\n')); err != nil {
		return backupManifest{}, err
	}
	if err := placeFile(staged, out, force); err != nil {
		return backupManifest{}, err
	}
	if err := placeFile(stagedManifest, manifestPath, true); err != nil {
		return backupManifest{}, fmt.Errorf("the database snapshot was written to %s but its manifest could not be: %w", out, err)
	}
	syncDir(filepath.Dir(out))
	return manifest, nil
}

// placeFile moves staged to target. Without replace it links instead of
// renaming, so an existing target is never overwritten, even one created
// after the up-front check. Filesystems without hard links (vfat, exFAT, SMB,
// some FUSE mounts) fall back to a rename after an existence check.
func placeFile(staged, target string, replace bool) error {
	if replace {
		return os.Rename(staged, target)
	}
	err := os.Link(staged, target)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; pass --force to replace it", target)
	}
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.EXDEV) {
		return fmt.Errorf("place %s: %w", target, err)
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		return fmt.Errorf("%s already exists; pass --force to replace it", target)
	}
	if renameErr := os.Rename(staged, target); renameErr != nil {
		return fmt.Errorf("place %s: %w", target, renameErr)
	}
	return nil
}

// realDir returns the symlink-resolved directory holding path, or the
// lexical directory when it cannot be resolved.
func realDir(path string) string {
	dir := filepath.Dir(path)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

func syncAndHash(path string) (int64, string, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return 0, "", err
	}
	if err := file.Sync(); err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func writeSyncedFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDir(dir string) {
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(left, right)
}

func printKeyReminder(w io.Writer, dataDir, masterKeyFile string) {
	masterKey := masterKeyFile
	if masterKey == "" {
		masterKey = filepath.Join(dataDir, "master.key") + " (or /etc/nexterm/master.key on systemd installs)"
	}
	fmt.Fprintln(w, "nexterm-server backup: NOT included, back these up separately and securely (not next to the database copy):")
	fmt.Fprintln(w, "nexterm-server backup:   vault master key:", masterKey)
	fmt.Fprintln(w, "nexterm-server backup:   TOTP key:", filepath.Join(dataDir, "totp.key"))
	fmt.Fprintln(w, "nexterm-server backup: without them saved credentials and two-factor secrets cannot be decrypted after a restore")
}

// printRemoteDumpGuidance names the native dump tool for a remote backend. It
// prints the host, port, user and database from the DSN but never a password,
// and connects to nothing.
func printRemoteDumpGuidance(w io.Writer, backend store.Backend, options backupOptions) {
	out := options.out
	if out == "" {
		out = "nexterm.dump"
	}
	fmt.Fprintf(w, "nexterm-server backup: the %s backend is not dumped by this command; back it up with the database's own tool, for example:\n", backend)
	host, port, user, database, socket := remoteDSNParts(backend, options.dbDSN)
	switch backend {
	case store.BackendPostgres:
		fmt.Fprintf(w, "  pg_dump --format=custom --file=%s --host=%s --port=%s --username=%s --dbname=%s\n",
			shellWord(out), shellWord(host), shellWord(port), shellWord(user), shellWord(database))
		fmt.Fprintln(w, "  restore: pg_restore --clean --if-exists --dbname=<database> <dump> while the service is stopped")
	default:
		tool := "mysqldump"
		if backend == store.BackendMariaDB {
			tool = "mariadb-dump"
		}
		endpoint := fmt.Sprintf("--host=%s --port=%s", shellWord(host), shellWord(port))
		if socket != "" {
			endpoint = "--socket=" + shellWord(socket)
		}
		fmt.Fprintf(w, "  %s --single-transaction --routines %s --user=%s --password %s > %s\n",
			tool, endpoint, shellWord(user), shellWord(database), shellWord(out))
		fmt.Fprintln(w, "  restore: load the dump with the mysql/mariadb client while the service is stopped")
	}
	fmt.Fprintln(w, "  the password is not shown; supply it through the tool's own prompt or password file")
	fmt.Fprintln(w, "nexterm-server backup: the dump does not contain the vault master key or <data-dir>/totp.key; back them up separately and securely")
}

func remoteDSNParts(backend store.Backend, dsn string) (host, port, user, database, socket string) {
	host, port, user, database = "<host>", "<port>", "<user>", "<database>"
	pick := func(value string, target *string) {
		if value != "" {
			*target = value
		}
	}
	if strings.TrimSpace(dsn) == "" {
		return
	}
	switch backend {
	case store.BackendPostgres:
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			return
		}
		pick(config.Host, &host)
		if config.Port != 0 {
			port = strconv.Itoa(int(config.Port))
		}
		pick(config.User, &user)
		pick(config.Database, &database)
	default:
		config, err := mysqldriver.ParseDSN(dsn)
		if err != nil {
			return
		}
		if config.Net == "unix" {
			socket = config.Addr
		} else if h, p, err := net.SplitHostPort(config.Addr); err == nil {
			pick(h, &host)
			pick(p, &port)
		} else {
			pick(config.Addr, &host)
		}
		pick(config.User, &user)
		pick(config.DBName, &database)
	}
	return
}

// shellWord quotes a value for a POSIX shell when it needs it; the four
// literal placeholders (<host>, <port>, <user>, <database>) are left for the operator to replace.
func shellWord(value string) string {
	if value == "" {
		return "''"
	}
	switch value {
	case "<host>", "<port>", "<user>", "<database>":
		return value
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:@=,+", r)) {
			return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
		}
	}
	return value
}
