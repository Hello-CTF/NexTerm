package production

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
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
	Revoked       int
}

func (report *productionHostKeyMigrationReport) countResult(result productionHostKeyMigrationResult) {
	switch result {
	case productionHostKeyMigrationCopied:
		report.Copied++
	case productionHostKeyMigrationExisting:
		report.Existing++
	case productionHostKeyMigrationConflict:
		report.Conflicts++
	}
}

func (result productionHostKeyMigrationResult) String() string {
	switch result {
	case productionHostKeyMigrationCopied:
		return "copied"
	case productionHostKeyMigrationExisting:
		return "existing"
	case productionHostKeyMigrationConflict:
		return "conflict"
	}
	return "unknown"
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
	if err := store.reconcileMirrors(ctx); err != nil {
		return nil, err
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

// knownHostMaterialTrusted reports whether the ledger row's complete key material is present and matches its
// stored fingerprint. Stale or malformed material — for example after a fingerprint rotation that did not
// carry the key — is never exposed for exact-match verification.
func knownHostMaterialTrusted(row store.KnownHostRow) bool {
	if row.Key == "" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(row.Key)
	if err != nil {
		return false
	}
	parsed, err := gossh.ParsePublicKey(decoded)
	if err != nil {
		return false
	}
	return gossh.FingerprintSHA256(parsed) == row.Fingerprint
}

// HostKeys answers from the owner-scoped ledger: the database holds the complete public key and metadata, so
// verification does not depend on the mirror file. Only rows with trusted key material are returned —
// material-less or stale rows still verify through ApprovedHostKeys and regain material on the next direct
// connect.
func (s *productionHostKeyStore) HostKeys(ctx context.Context, host string, port int) ([]ssh.HostKey, error) {
	if _, err := s.requestOwner(ctx); err != nil {
		return nil, err
	}
	rows, err := s.database.KnownHostList(ctx)
	if err != nil {
		return nil, err
	}
	var known []ssh.HostKey
	for _, row := range rows {
		if row.Host == host && int(row.Port) == port && knownHostMaterialTrusted(row) {
			known = append(known, ssh.HostKey{Host: row.Host, Port: int(row.Port), KeyType: row.KeyType, Fingerprint: row.Fingerprint, Key: row.Key})
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
			approved = append(approved, ssh.HostKey{Host: row.Host, Port: int(row.Port), KeyType: row.KeyType, Fingerprint: row.Fingerprint, Key: row.Key})
		}
	}
	return approved, nil
}

// PutHostKey commits the ledger first and the mirror second. A failed mirror write compensates the ledger,
// so the database stays the single source of truth and the mirror never runs ahead of it. The ledger accept
// atomically captures the actual previous row, and the compensation is conditional on the accepted outcome:
// an interleaved independent trust decision can never be rolled back by mistake.
func (s *productionHostKeyStore) PutHostKey(ctx context.Context, key ssh.HostKey, replace bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.requestOwner(ctx); err != nil {
		return err
	}
	files, _, err := s.fileFor(ctx)
	if err != nil {
		return err
	}
	accepted, previous, err := s.database.KnownHostAcceptForMirror(ctx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint, key.Key, replace)
	if errors.Is(err, store.ErrKnownHostConflict) {
		return ssh.ErrHostKeyConflict
	}
	if err != nil {
		return err
	}
	mirrorReplace := replace
	if !mirrorReplace && previous != nil && previous.Fingerprint == key.Fingerprint {
		// The ledger already trusts exactly this key, so a different-key mirror entry for the same endpoint
		// is a stale leftover (for example a replacement confirmed through the fingerprint-only IPC path);
		// overwrite it instead of failing on a conflict the ledger has already resolved.
		mirrorReplace = true
	}
	if err := files.PutHostKey(ctx, key, mirrorReplace); err != nil {
		if _, compensateErr := s.database.KnownHostCompensateAccept(ctx, key.Host, int32(key.Port), key.KeyType, accepted, previous); compensateErr != nil {
			return errors.Join(err, fmt.Errorf("compensate SSH host key ledger: %w", compensateErr))
		}
		return err
	}
	return nil
}

type productionHostKeyImportEntry struct {
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	KeyType       string   `json:"keyType"`
	Fingerprint   string   `json:"fingerprint"`
	Disposition   string   `json:"disposition"`
	Owners        []string `json:"owners,omitempty"`
	State         string   `json:"state"`
	Result        string   `json:"result,omitempty"`
	RevokedOwners []string `json:"revokedOwners,omitempty"`
}

type productionHostKeyImportState struct {
	Version int                            `json:"version"`
	Entries []productionHostKeyImportEntry `json:"entries"`
}

func productionHostKeyImportIdentity(key ssh.HostKey) string {
	return fmt.Sprintf("%s|%d|%s|%s", key.Host, key.Port, key.KeyType, key.Fingerprint)
}

func (s *productionHostKeyStore) loadImportState() (map[string]*productionHostKeyImportEntry, error) {
	entries := make(map[string]*productionHostKeyImportEntry)
	data, existed, err := readProductionHostKeyFile(s.path + ".owner-import-state.json")
	if err != nil || !existed {
		return entries, err
	}
	var state productionHostKeyImportState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode host key import state: %w", err)
	}
	for index := range state.Entries {
		entry := &state.Entries[index]
		entries[productionHostKeyImportIdentity(ssh.HostKey{Host: entry.Host, Port: entry.Port, KeyType: entry.KeyType, Fingerprint: entry.Fingerprint})] = entry
	}
	return entries, nil
}

func (s *productionHostKeyStore) storeImportState(entries map[string]*productionHostKeyImportEntry) error {
	state := productionHostKeyImportState{Version: 1, Entries: make([]productionHostKeyImportEntry, 0, len(entries))}
	for _, entry := range entries {
		state.Entries = append(state.Entries, *entry)
	}
	sort.Slice(state.Entries, func(i, j int) bool {
		return productionHostKeyImportIdentity(ssh.HostKey{Host: state.Entries[i].Host, Port: state.Entries[i].Port, KeyType: state.Entries[i].KeyType, Fingerprint: state.Entries[i].Fingerprint}) <
			productionHostKeyImportIdentity(ssh.HostKey{Host: state.Entries[j].Host, Port: state.Entries[j].Port, KeyType: state.Entries[j].KeyType, Fingerprint: state.Entries[j].Fingerprint})
	})
	return atomicfile.Write(s.path+".owner-import-state.json", 0o600, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(state)
	})
}

func (s *productionHostKeyStore) migrateLegacy(ctx context.Context) (productionHostKeyMigrationReport, error) {
	if s.shared {
		return s.migrateSharedLegacy(ctx)
	}
	var report productionHostKeyMigrationReport
	// The legacy import is one-time: the marker is written only after the whole batch succeeds, so a crash
	// mid-import resumes idempotently, and once the import completes, entries revoked since can never be
	// resurrected from the untouched legacy file on a later startup.
	markerPath := s.path + ".owner-import-done"
	if _, err := os.Stat(markerPath); err == nil {
		return report, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return report, err
	}
	data, existed, err := readProductionHostKeyFile(s.path)
	if err != nil || !existed {
		return report, err
	}
	keys, err := s.files.HostKeySnapshot(ctx)
	if err != nil {
		return report, err
	}
	entries, err := s.loadImportState()
	if err != nil {
		return report, err
	}
	// Record every legacy entry's original disposition before any import runs. Resuming batches use
	// these persisted assignments instead of recomputing ownership from live rows, so an entry whose
	// mapped owner revoked it while the batch was pending is never re-imported for that owner.
	for _, key := range keys {
		identity := productionHostKeyImportIdentity(key)
		if _, ok := entries[identity]; ok {
			continue
		}
		rows, err := s.database.KnownHostListForMigration(ctx)
		if err != nil {
			return report, err
		}
		mapped := []string{}
		for _, row := range rows {
			if row.OwnerID == "" {
				continue
			}
			if row.Host == key.Host && int(row.Port) == key.Port && row.KeyType == key.KeyType && row.Fingerprint == key.Fingerprint {
				mapped = append(mapped, row.OwnerID)
			}
		}
		sort.Strings(mapped)
		entry := &productionHostKeyImportEntry{
			Host: key.Host, Port: key.Port, KeyType: key.KeyType, Fingerprint: key.Fingerprint,
			State: "pending",
		}
		if len(mapped) > 0 {
			entry.Disposition = "owners"
			entry.Owners = mapped
		} else {
			entry.Disposition = "fallback"
		}
		entries[identity] = entry
	}
	// Preflight: postpone the whole batch while any pending fallback entry has no active superadmin to
	// receive it. The marker is never written for a postponed batch — a later startup retries once an
	// administrator appears — and a partially imported batch can never mix with later revocations.
	fallbackPending := 0
	for _, entry := range entries {
		if entry.State == "pending" && entry.Disposition == "fallback" {
			fallbackPending++
		}
	}
	if fallbackPending > 0 {
		fallbackOwner, err := s.firstActiveSuperadmin(ctx)
		if err != nil {
			return report, err
		}
		if fallbackOwner == "" {
			if err := s.storeImportState(entries); err != nil {
				return report, err
			}
			report.Skipped = fallbackPending
			return report, nil
		}
	}
	report.BackupCreated, err = backupLegacyProductionHostKeyFile(s.path, data)
	if err != nil {
		return report, err
	}
	keysByIdentity := make(map[string]ssh.HostKey, len(keys))
	for _, key := range keys {
		keysByIdentity[productionHostKeyImportIdentity(key)] = key
	}
	for identity, entry := range entries {
		if entry.State != "pending" {
			continue
		}
		key, ok := keysByIdentity[identity]
		if !ok {
			entry.State = "done"
			continue
		}
		if entry.Disposition == "fallback" {
			fallbackOwner, err := s.firstActiveSuperadmin(ctx)
			if err != nil {
				return report, err
			}
			if fallbackOwner == "" {
				continue
			}
			result, err := s.importLegacyKey(ctx, fallbackOwner, key)
			if err != nil {
				return report, err
			}
			report.countResult(result)
			entry.State = "done"
			entry.Result = result.String()
			continue
		}
		allRevoked := true
		for _, ownerID := range entry.Owners {
			if slices.Contains(entry.RevokedOwners, ownerID) {
				continue
			}
			ownerCtx := ipc.WithUserID(ctx, ownerID)
			_, found, err := s.database.KnownHostGet(ownerCtx, entry.Host, int32(entry.Port), entry.KeyType)
			if err != nil {
				return report, err
			}
			if !found {
				entry.RevokedOwners = append(entry.RevokedOwners, ownerID)
				report.Revoked++
				continue
			}
			allRevoked = false
			result, err := s.importLegacyKey(ownerCtx, ownerID, key)
			if err != nil {
				return report, err
			}
			report.countResult(result)
			entry.Result = result.String()
		}
		if allRevoked {
			entry.State = "revoked"
		} else {
			entry.State = "done"
		}
	}
	if err := s.storeImportState(entries); err != nil {
		return report, err
	}
	remainingPending := 0
	for _, entry := range entries {
		if entry.State == "pending" {
			remainingPending++
		}
	}
	if remainingPending == 0 {
		if err := atomicfile.Write(markerPath, 0o600, func(writer io.Writer) error { return nil }); err != nil {
			return report, err
		}
	}
	return report, nil
}

// migrateSharedLegacy imports the desktop shared trust file into the owner-less ledger so the database, not
// the file, decides verification. The import is one-time: a marker records that the legacy file has been
// consumed, so mirror-only leftovers (for example a revocation whose mirror rewrite was interrupted) can
// never recreate trust on a later startup. The file stays in place as the mirror: no backup is taken and no
// entry is rewritten, and re-running the import is idempotent. Ledger rows win over conflicting file entries.
func (s *productionHostKeyStore) migrateSharedLegacy(ctx context.Context) (productionHostKeyMigrationReport, error) {
	var report productionHostKeyMigrationReport
	markerPath := s.path + ".shared-import-done"
	if _, err := os.Stat(markerPath); err == nil {
		return report, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return report, err
	}
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
			if ledger.Key == "" && key.Key != "" {
				if err := s.database.KnownHostFillKeyMaterial(ctx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint, key.Key); err != nil {
					return report, err
				}
			}
			report.Existing++
		case ledgerExists:
			report.Conflicts++
		default:
			if _, err := s.database.KnownHostAcceptWithKey(ctx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint, key.Key); err != nil {
				return report, err
			}
			report.Copied++
		}
	}
	if err := atomicfile.Write(markerPath, 0o600, func(writer io.Writer) error { return nil }); err != nil {
		return report, err
	}
	return report, nil
}

// reconcileMirrors rewrites every mirror file to match the owner-scoped ledger, healing interrupted writes
// in the database-wins direction: ledger rows with trusted material missing from a mirror are written, and
// mirror-only leftovers (for example a revocation whose mirror rewrite was interrupted) are removed. Files
// whose content already matches the ledger are left byte-identical.
func (s *productionHostKeyStore) reconcileMirrors(ctx context.Context) error {
	if s.shared {
		return s.reconcileMirror(ctx, ctx)
	}
	rows, err := s.database.KnownHostListForMigration(ctx)
	if err != nil {
		return err
	}
	owners := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.OwnerID != "" {
			owners[row.OwnerID] = struct{}{}
		}
	}
	for ownerID := range owners {
		if err := s.reconcileMirror(ctx, ipc.WithUserID(ctx, ownerID)); err != nil {
			return err
		}
	}
	// Mirror files whose owner no longer holds any ledger row are leftovers of fully revoked trust — for
	// example a crash between the last row's delete and the mirror rewrite. Their names are derived from
	// the owner ID hash, so any u-*.json outside the expected set belongs to a row-less owner: remove it.
	expected := make(map[string]struct{}, len(owners))
	for ownerID := range owners {
		digest := sha256.Sum256([]byte(ownerID))
		expected[fmt.Sprintf("u-%x.json", digest)] = struct{}{}
	}
	entries, err := os.ReadDir(s.accountDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "u-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if _, ok := expected[name]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(s.accountDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *productionHostKeyStore) reconcileMirror(ctx context.Context, ownerCtx context.Context) error {
	ownerID := ipc.OwnerID(ownerCtx)
	rows, err := s.database.KnownHostList(ownerCtx)
	if err != nil {
		return err
	}
	desired := make(map[productionHostKeyIdentity]ssh.HostKey, len(rows))
	for _, row := range rows {
		if row.OwnerID != ownerID || !knownHostMaterialTrusted(row) {
			continue
		}
		identity := productionHostKeyIdentity{host: row.Host, port: int(row.Port), keyType: row.KeyType, fingerprint: row.Fingerprint}
		desired[identity] = ssh.HostKey{Host: row.Host, Port: int(row.Port), KeyType: row.KeyType, Fingerprint: row.Fingerprint, Key: row.Key}
	}
	s.mu.Lock()
	files, path, err := s.fileFor(ownerCtx)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	mirrored, err := files.HostKeySnapshot(ctx)
	if err != nil {
		return err
	}
	current := make(map[productionHostKeyIdentity]ssh.HostKey, len(mirrored))
	for _, key := range mirrored {
		identity := productionHostKeyIdentity{host: key.Host, port: key.Port, keyType: key.KeyType, fingerprint: key.Fingerprint}
		current[identity] = key
	}
	if maps.Equal(current, desired) {
		return nil
	}
	keys := make([]ssh.HostKey, 0, len(desired))
	for _, key := range desired {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Host != keys[j].Host {
			return keys[i].Host < keys[j].Host
		}
		if keys[i].Port != keys[j].Port {
			return keys[i].Port < keys[j].Port
		}
		return keys[i].KeyType < keys[j].KeyType
	})
	return writeProductionHostKeyFile(path, productionHostKeyFile{Version: 1, Keys: keys})
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
	if ledgerExists && ledger.Key == "" && key.Key != "" {
		if err := s.database.KnownHostFillKeyMaterial(ownerCtx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint, key.Key); err != nil {
			return productionHostKeyMigrationConflict, err
		}
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
		if _, err := s.database.KnownHostAcceptWithKey(ownerCtx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint, key.Key); err != nil {
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
