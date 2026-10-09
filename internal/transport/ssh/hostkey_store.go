package ssh

import (
	"context"
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
)

type MemoryHostKeyStore struct {
	mu   sync.RWMutex
	keys []HostKey
}

func NewMemoryHostKeyStore() *MemoryHostKeyStore {
	return &MemoryHostKeyStore{}
}

func (s *MemoryHostKeyStore) HostKeys(ctx context.Context, host string, port int) ([]HostKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return selectHostKeys(s.keys, host, port), nil
}

func (s *MemoryHostKeyStore) PutHostKey(ctx context.Context, key HostKey, replace bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !key.valid() {
		return fmt.Errorf("invalid host key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return putHostKey(&s.keys, key, replace)
}

type FileHostKeyStore struct {
	path string
	mu   sync.Mutex
}

func NewFileHostKeyStore(path string) (*FileHostKeyStore, error) {
	if path == "" {
		return nil, fmt.Errorf("host key store path is empty")
	}
	return &FileHostKeyStore{path: filepath.Clean(path)}, nil
}

func (s *FileHostKeyStore) HostKeys(ctx context.Context, host string, port int) ([]HostKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	return selectHostKeys(keys, host, port), nil
}

func (s *FileHostKeyStore) PutHostKey(ctx context.Context, key HostKey, replace bool) error {
	if !key.valid() {
		return fmt.Errorf("invalid host key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.read(ctx)
	if err != nil {
		return err
	}
	if err := putHostKey(&keys, key, replace); err != nil {
		return err
	}
	return s.write(ctx, keys)
}

type diskHostKeys struct {
	Version int       `json:"version"`
	Keys    []HostKey `json:"keys"`
}

func (s *FileHostKeyStore) read(ctx context.Context) ([]HostKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var disk diskHostKeys
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("decode host key store: %w", err)
	}
	if disk.Version != 1 {
		return nil, fmt.Errorf("unsupported host key store version %d", disk.Version)
	}
	for _, key := range disk.Keys {
		if !key.valid() {
			return nil, fmt.Errorf("invalid entry in host key store")
		}
	}
	return disk.Keys, nil
}

func (s *FileHostKeyStore) write(ctx context.Context, keys []HostKey) error {
	if err := ctx.Err(); err != nil {
		return err
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
	return atomicfile.Write(s.path, 0o600, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(diskHostKeys{Version: 1, Keys: keys}); err != nil {
			return err
		}
		return ctx.Err()
	})
}

func selectHostKeys(keys []HostKey, host string, port int) []HostKey {
	var selected []HostKey
	for _, key := range keys {
		if key.Host == host && key.Port == port {
			selected = append(selected, key)
		}
	}
	return selected
}

func putHostKey(keys *[]HostKey, key HostKey, replace bool) error {
	existing := selectHostKeys(*keys, key.Host, key.Port)
	var existingSameType []HostKey
	for _, candidate := range existing {
		if candidate.KeyType == key.KeyType {
			existingSameType = append(existingSameType, candidate)
		}
	}
	if len(existingSameType) > 0 && !replace {
		for _, candidate := range existingSameType {
			if candidate.Key == key.Key {
				return nil
			}
		}
		return ErrHostKeyConflict
	}
	if replace {
		kept := (*keys)[:0]
		for _, candidate := range *keys {
			if candidate.Host != key.Host || candidate.Port != key.Port || candidate.KeyType != key.KeyType {
				kept = append(kept, candidate)
			}
		}
		*keys = kept
	}
	*keys = append(*keys, key)
	return nil
}
