package production

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	"gorm.io/gorm"
)

type productionHostKeyStore struct {
	database     *store.Store
	files        *ssh.FileHostKeyStore
	path         string
	accountDir   string
	accountFiles map[string]*ssh.FileHostKeyStore
	shared       bool
	migration    productionHostKeyMigrationReport
	mu           sync.Mutex
}

type productionHostKeyFile struct {
	Version int           `json:"version"`
	Keys    []ssh.HostKey `json:"keys"`
}

type productionHostKeyMigrationReport struct {
	BackupCreated bool
	Copied        int
	Existing      int
	Conflicts     int
	Skipped       int
}

type productionHostKeyIdentity struct {
	host        string
	port        int
	keyType     string
	fingerprint string
}

type productionHostKeyMigrationResult int

const (
	productionHostKeyMigrationCopied productionHostKeyMigrationResult = iota
	productionHostKeyMigrationExisting
	productionHostKeyMigrationConflict
)

func newProductionHostKeyStore(ctx context.Context, path string, database *store.Store, shared bool) (*productionHostKeyStore, error) {
	files, err := ssh.NewFileHostKeyStore(path)
	if err != nil {
		return nil, err
	}
	store := &productionHostKeyStore{database: database, files: files, path: path, shared: shared}
	if !shared {
		store.accountDir = filepath.Join(filepath.Dir(path), "known_hosts")
		store.accountFiles = make(map[string]*ssh.FileHostKeyStore)
		if err := store.ensureAccountDir(); err != nil {
			return nil, err
		}
	}
	report, err := store.migrateLegacy(ctx)
	if err != nil {
		return nil, err
	}
	store.migration = report
	return store, nil
}

func productionHostKeyOwner(ctx context.Context, shared bool) (string, error) {
	if shared {
		return "", nil
	}
	if ownerID, ok := ipc.UserIDFromContext(ctx); ok {
		return ownerID, nil
	}
	return "", ipc.NewError(ipc.CodeForbidden, "SSH 主机信任缺少账号身份")
}

func (s *productionHostKeyStore) requestOwner(ctx context.Context) (string, error) {
	return productionHostKeyOwner(ctx, s.shared)
}

func productionKnownHostList(ctx context.Context, database *store.Store, hostKeys *productionHostKeyStore, shared bool) ([]store.KnownHostRow, error) {
	if hostKeys != nil {
		if _, err := hostKeys.requestOwner(ctx); err != nil {
			return nil, err
		}
	} else if _, err := productionHostKeyOwner(ctx, shared); err != nil {
		return nil, err
	}
	return database.KnownHostList(ctx)
}

func productionKnownHostAccept(ctx context.Context, database *store.Store, hostKeys *productionHostKeyStore, shared bool, host string, port int32, keyType, fingerprint string) error {
	if hostKeys != nil {
		if _, err := hostKeys.requestOwner(ctx); err != nil {
			return err
		}
	} else if _, err := productionHostKeyOwner(ctx, shared); err != nil {
		return err
	}
	return database.KnownHostAccept(ctx, host, port, keyType, fingerprint)
}

func productionKnownHostRemove(ctx context.Context, database *store.Store, hostKeys *productionHostKeyStore, shared bool, id string) error {
	if hostKeys != nil {
		return hostKeys.Remove(ctx, id)
	}
	if _, err := productionHostKeyOwner(ctx, shared); err != nil {
		return err
	}
	return database.KnownHostRemove(ctx, id)
}

func (s *productionHostKeyStore) ensureAccountDir() error {
	if err := os.MkdirAll(s.accountDir, 0o700); err != nil {
		return err
	}
	return os.Chmod(s.accountDir, 0o700)
}

func (s *productionHostKeyStore) fileFor(ctx context.Context) (*ssh.FileHostKeyStore, string, error) {
	if s.shared {
		return s.files, s.path, nil
	}
	ownerID, err := s.requestOwner(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.ensureAccountDir(); err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256([]byte(ownerID))
	path := filepath.Join(s.accountDir, fmt.Sprintf("u-%x.json", digest))
	files := s.accountFiles[ownerID]
	if files == nil {
		files, err = ssh.NewFileHostKeyStore(path)
		if err != nil {
			return nil, "", err
		}
		s.accountFiles[ownerID] = files
	}
	if _, err := os.Stat(path); err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, "", err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	}
	return files, path, nil
}

// HostKeys returns the mirror entries that the owner-scoped ledger trusts: verification reads trust from the
// database while the file only supplies complete key material. Mirror-only leftovers — for example from an
// interrupted write — are never returned and cannot silently extend trust.
func (s *productionHostKeyStore) HostKeys(ctx context.Context, host string, port int) ([]ssh.HostKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.requestOwner(ctx); err != nil {
		return nil, err
	}
	rows, err := s.database.KnownHostList(ctx)
	if err != nil {
		return nil, err
	}
	trusted := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Host == host && int(row.Port) == port {
			trusted[row.KeyType+"\x00"+row.Fingerprint] = struct{}{}
		}
	}
	files, _, err := s.fileFor(ctx)
	if err != nil {
		return nil, err
	}
	mirrored, err := files.HostKeys(ctx, host, port)
	if err != nil {
		return nil, err
	}
	var known []ssh.HostKey
	for _, key := range mirrored {
		if _, ok := trusted[key.KeyType+"\x00"+key.Fingerprint]; ok {
			known = append(known, key)
		}
	}
	return known, nil
}

func (s *productionHostKeyStore) ApprovedHostKeys(ctx context.Context, host string, port int) ([]ssh.HostKey, error) {
	if _, err := s.requestOwner(ctx); err != nil {
		return nil, err
	}
	rows, err := s.database.KnownHostList(ctx)
	if err != nil {
		return nil, err
	}
	var approved []ssh.HostKey
	for _, row := range rows {
		if row.Host == host && int(row.Port) == port {
			approved = append(approved, ssh.HostKey{Host: row.Host, Port: int(row.Port), KeyType: row.KeyType, Fingerprint: row.Fingerprint})
		}
	}
	return approved, nil
}

// PutHostKey commits the ledger first and the mirror second. A failed mirror write compensates the ledger,
// so the database stays the single source of truth and the mirror never runs ahead of it.
func (s *productionHostKeyStore) PutHostKey(ctx context.Context, key ssh.HostKey, replace bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.requestOwner(ctx); err != nil {
		return err
	}
	previous, existed, err := s.database.KnownHostGet(ctx, key.Host, int32(key.Port), key.KeyType)
	if err != nil {
		return err
	}
	if existed && !replace && previous.Fingerprint != key.Fingerprint {
		return ssh.ErrHostKeyConflict
	}
	files, _, err := s.fileFor(ctx)
	if err != nil {
		return err
	}
	if err := s.database.KnownHostAccept(ctx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint); err != nil {
		return err
	}
	mirrorReplace := replace
	if !mirrorReplace && existed && previous.Fingerprint == key.Fingerprint {
		// The ledger already trusts exactly this key, so a different-key mirror entry for the same endpoint
		// is a stale leftover (for example a replacement confirmed through the fingerprint-only IPC path);
		// overwrite it instead of failing on a conflict the ledger has already resolved.
		mirrorReplace = true
	}
	if err := files.PutHostKey(ctx, key, mirrorReplace); err != nil {
		var rollback *store.KnownHostRow
		if existed {
			rollback = &previous
		}
		if compensateErr := s.database.KnownHostCompensateAccept(ctx, key.Host, int32(key.Port), key.KeyType, rollback); compensateErr != nil {
			return errors.Join(err, fmt.Errorf("compensate SSH host key ledger: %w", compensateErr))
		}
		return err
	}
	return nil
}

func (s *productionHostKeyStore) migrateLegacy(ctx context.Context) (productionHostKeyMigrationReport, error) {
	if s.shared {
		return s.migrateSharedLegacy(ctx)
	}
	var report productionHostKeyMigrationReport
	data, existed, err := readProductionHostKeyFile(s.path)
	if err != nil || !existed {
		return report, err
	}
	report.BackupCreated, err = backupLegacyProductionHostKeyFile(s.path, data)
	if err != nil {
		return report, err
	}
	keys, err := s.files.HostKeySnapshot(ctx)
	if err != nil {
		return report, err
	}
	rows, err := s.database.KnownHostListForMigration(ctx)
	if err != nil {
		return report, err
	}
	owners := make(map[productionHostKeyIdentity][]string)
	for _, row := range rows {
		if row.OwnerID == "" {
			continue
		}
		identity := productionHostKeyIdentity{host: row.Host, port: int(row.Port), keyType: row.KeyType, fingerprint: row.Fingerprint}
		owners[identity] = append(owners[identity], row.OwnerID)
	}
	fallbackOwner := ""
	fallbackResolved := false
	for _, key := range keys {
		identity := productionHostKeyIdentity{host: key.Host, port: key.Port, keyType: key.KeyType, fingerprint: key.Fingerprint}
		mappedOwners := owners[identity]
		if len(mappedOwners) == 0 {
			if !fallbackResolved {
				fallbackOwner, err = s.firstActiveSuperadmin(ctx)
				fallbackResolved = true
				if err != nil {
					return report, err
				}
			}
			if fallbackOwner == "" {
				report.Skipped++
				continue
			}
			mappedOwners = []string{fallbackOwner}
		}
		sort.Strings(mappedOwners)
		for _, ownerID := range mappedOwners {
			result, err := s.importLegacyKey(ctx, ownerID, key)
			if err != nil {
				return report, err
			}
			switch result {
			case productionHostKeyMigrationCopied:
				report.Copied++
			case productionHostKeyMigrationExisting:
				report.Existing++
			case productionHostKeyMigrationConflict:
				report.Conflicts++
			}
		}
	}
	return report, nil
}

// migrateSharedLegacy imports the desktop shared trust file into the owner-less ledger so the database, not
// the file, decides verification. The file stays in place as the mirror: no backup is taken and no entry is
// rewritten, and re-running the import is idempotent. Ledger rows win over conflicting file entries.
func (s *productionHostKeyStore) migrateSharedLegacy(ctx context.Context) (productionHostKeyMigrationReport, error) {
	var report productionHostKeyMigrationReport
	keys, err := s.files.HostKeySnapshot(ctx)
	if err != nil {
		return report, err
	}
	for _, key := range keys {
		ledger, ledgerExists, err := s.database.KnownHostGet(ctx, key.Host, int32(key.Port), key.KeyType)
		if err != nil {
			return report, err
		}
		switch {
		case ledgerExists && ledger.Fingerprint == key.Fingerprint:
			report.Existing++
		case ledgerExists:
			report.Conflicts++
		default:
			if err := s.database.KnownHostAccept(ctx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint); err != nil {
				return report, err
			}
			report.Copied++
		}
	}
	return report, nil
}

func backupLegacyProductionHostKeyFile(path string, data []byte) (bool, error) {
	backupPath := path + ".pre-owner-migration.bak"
	backup, err := os.ReadFile(backupPath)
	if err == nil {
		if !bytes.Equal(backup, data) {
			return false, fmt.Errorf("legacy SSH host key backup conflicts with the original file")
		}
		if err := os.Chmod(backupPath, 0o600); err != nil {
			return false, err
		}
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := atomicfile.Write(backupPath, 0o600, func(writer io.Writer) error {
		_, err := writer.Write(data)
		return err
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *productionHostKeyStore) firstActiveSuperadmin(ctx context.Context) (string, error) {
	var user store.AppUser
	err := s.database.GORM().WithContext(ctx).
		Select("id").
		Where("role = ? AND state = ?", "superadmin", "active").
		Order("created_at").Order("id").
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return user.ID, nil
}

func (s *productionHostKeyStore) importLegacyKey(ctx context.Context, ownerID string, key ssh.HostKey) (productionHostKeyMigrationResult, error) {
	ownerCtx := ipc.WithUserID(ctx, ownerID)
	ledger, ledgerExists, err := s.database.KnownHostGet(ownerCtx, key.Host, int32(key.Port), key.KeyType)
	if err != nil {
		return productionHostKeyMigrationConflict, err
	}
	if ledgerExists && ledger.Fingerprint != key.Fingerprint {
		return productionHostKeyMigrationConflict, nil
	}
	files, path, err := s.fileFor(ownerCtx)
	if err != nil {
		return productionHostKeyMigrationConflict, err
	}
	previous, fileExisted, err := readProductionHostKeyFile(path)
	if err != nil {
		return productionHostKeyMigrationConflict, err
	}
	before, err := files.HostKeySnapshot(ctx)
	if err != nil {
		return productionHostKeyMigrationConflict, err
	}
	alreadyPresent := false
	for _, candidate := range before {
		if candidate == key {
			alreadyPresent = true
			break
		}
	}
	if !alreadyPresent {
		if err := files.PutHostKey(ctx, key, false); errors.Is(err, ssh.ErrHostKeyConflict) {
			return productionHostKeyMigrationConflict, nil
		} else if err != nil {
			return productionHostKeyMigrationConflict, err
		}
	}
	if !ledgerExists {
		if err := s.database.KnownHostAccept(ownerCtx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint); err != nil {
			if !alreadyPresent {
				if rollbackErr := restoreProductionHostKeyFile(path, previous, fileExisted); rollbackErr != nil {
					return productionHostKeyMigrationConflict, errors.Join(err, fmt.Errorf("restore SSH host key file: %w", rollbackErr))
				}
			}
			return productionHostKeyMigrationConflict, err
		}
	}
	if alreadyPresent {
		return productionHostKeyMigrationExisting, nil
	}
	return productionHostKeyMigrationCopied, nil
}

func readProductionHostKeyFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func restoreProductionHostKeyFile(path string, data []byte, existed bool) error {
	if !existed {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		_, err := writer.Write(data)
		return err
	})
}

// Remove deletes from the ledger first and rewrites the mirror second. A failed mirror rewrite restores the
// ledger row and drops its tombstone, so a failed revocation never half-persists.
func (s *productionHostKeyStore) Remove(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.requestOwner(ctx); err != nil {
		return err
	}
	_, path, err := s.fileFor(ctx)
	if err != nil {
		return err
	}
	removed, found, err := s.database.KnownHostRemoveForMirror(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return s.database.KnownHostRemove(ctx, id)
	}
	restore := func(cause error) error {
		if restoreErr := s.database.KnownHostRestoreRemoved(ctx, removed); restoreErr != nil {
			return errors.Join(cause, fmt.Errorf("restore SSH host key ledger: %w", restoreErr))
		}
		return cause
	}
	data, _, err := readProductionHostKeyFile(path)
	if err != nil {
		return restore(err)
	}
	if len(data) == 0 {
		return nil
	}
	var file productionHostKeyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return restore(fmt.Errorf("decode host key store: %w", err))
	}
	kept := file.Keys[:0]
	for _, key := range file.Keys {
		if key.Host == removed.Host && key.Port == int(removed.Port) && key.KeyType == removed.KeyType && key.Fingerprint == removed.Fingerprint {
			continue
		}
		kept = append(kept, key)
	}
	file.Version = 1
	file.Keys = kept
	if err := writeProductionHostKeyFile(path, file); err != nil {
		return restore(err)
	}
	return nil
}

func writeProductionHostKeyFile(path string, file productionHostKeyFile) error {
	return atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(file)
	})
}

var _ ssh.HostKeyStore = (*productionHostKeyStore)(nil)
var _ ssh.HostKeyApprovalStore = (*productionHostKeyStore)(nil)
