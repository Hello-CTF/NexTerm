package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/atomicfile"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
)

type productionHostKeyStore struct {
	database *store.Store
	files    *ssh.FileHostKeyStore
	path     string
	mu       sync.Mutex
}

type productionHostKeyFile struct {
	Version int           `json:"version"`
	Keys    []ssh.HostKey `json:"keys"`
}

func newProductionHostKeyStore(path string, database *store.Store) (*productionHostKeyStore, error) {
	files, err := ssh.NewFileHostKeyStore(path)
	if err != nil {
		return nil, err
	}
	return &productionHostKeyStore{database: database, files: files, path: path}, nil
}

func (s *productionHostKeyStore) HostKeys(ctx context.Context, host string, port int) ([]ssh.HostKey, error) {
	return s.files.HostKeys(ctx, host, port)
}

func (s *productionHostKeyStore) ApprovedHostKeys(ctx context.Context, host string, port int) ([]ssh.HostKey, error) {
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
	if err := s.files.PutHostKey(ctx, key, replace); err != nil {
		return err
	}
	return s.database.KnownHostAccept(ctx, key.Host, int32(key.Port), key.KeyType, key.Fingerprint)
}

func (s *productionHostKeyStore) Remove(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	data, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
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
		if err := writeProductionHostKeyFile(s.path, file); err != nil {
			return err
		}
	}
	return s.database.KnownHostRemove(ctx, id)
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
