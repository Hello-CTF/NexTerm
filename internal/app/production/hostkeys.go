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
		report, err := store.migrateLegacy(ctx)
		if err != nil {
			return nil, err
		}
		store.migration = report
	}
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

func (s *productionHostKeyStore) HostKeys(ctx context.Context, host string, port int) ([]ssh.HostKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, _, err := s.fileFor(ctx)
	if err != nil {
		return nil, err
	}
	return files.HostKeys(ctx, host, port)
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

func (s *productionHostKeyStore) PutHostKey(ctx context.Context, key ssh.HostKey, replace bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, path, err := s.fileFor(ctx)
	if err != nil {
		return err
	}
	previous, existed, err := readProductionHostKeyFile(path)
	if err != nil {
		return err
	}
	if err := files.PutHostKey(ctx, key, replace); err != nil {
		return err
	}
	if err := s.database.KnownHostAccept(ctx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint); err != nil {
		if rollbackErr := restoreProductionHostKeyFile(path, previous, existed); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore SSH host key file: %w", rollbackErr))
		}
		return err
	}
	return nil
}

func (s *productionHostKeyStore) migrateLegacy(ctx context.Context) (productionHostKeyMigrationReport, error) {
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

func (s *productionHostKeyStore) Remove(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, path, err := s.fileFor(ctx)
	if err != nil {
		return err
	}
	rows, err := s.database.KnownHostList(ctx)
	if err != nil {
		return err
	}
	var selected *store.KnownHostRow
	for index := range rows {
		if rows[index].ID == id {
			selected = &rows[index]
			break
		}
	}
	if selected == nil {
		return s.database.KnownHostRemove(ctx, id)
	}
	data, existed, err := readProductionHostKeyFile(path)
	if err != nil {
		return err
	}
	fileChanged := false
	if len(data) != 0 {
		var file productionHostKeyFile
		if err := json.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("decode host key store: %w", err)
		}
		kept := file.Keys[:0]
		for _, key := range file.Keys {
			if key.Host == selected.Host && key.Port == int(selected.Port) && key.KeyType == selected.KeyType && key.Fingerprint == selected.Fingerprint {
				continue
			}
			kept = append(kept, key)
		}
		file.Version = 1
		file.Keys = kept
		if err := writeProductionHostKeyFile(path, file); err != nil {
			return err
		}
		fileChanged = true
	}
	if err := s.database.KnownHostRemove(ctx, id); err != nil {
		if fileChanged {
			if rollbackErr := restoreProductionHostKeyFile(path, data, existed); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("restore SSH host key file: %w", rollbackErr))
			}
		}
		return err
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
