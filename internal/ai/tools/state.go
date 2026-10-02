package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"sync"
)

type fileVersion struct {
	Exists bool
	Size   int64
	Hash   string
}

func versionOf(content []byte) fileVersion {
	hash := sha256.Sum256(content)
	return fileVersion{Exists: true, Size: int64(len(content)), Hash: hex.EncodeToString(hash[:])}
}

type jobState struct {
	mu           sync.Mutex
	reads        map[string]fileVersion
	preparations map[string]*preparedChange
	todos        []TodoItem
	plan         string
}

func newJobState() *jobState {
	return &jobState{reads: make(map[string]fileVersion), preparations: make(map[string]*preparedChange)}
}

func (s *jobState) readVersion(key string) (fileVersion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version, ok := s.reads[key]
	return version, ok
}

func (s *jobState) rememberRead(key string, version fileVersion) {
	s.mu.Lock()
	s.reads[key] = version
	s.mu.Unlock()
}

func (s *jobState) putPreparation(id string, change *preparedChange) {
	s.mu.Lock()
	s.preparations[id] = change
	s.mu.Unlock()
}

func (s *jobState) preparation(id string) (*preparedChange, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change, ok := s.preparations[id]
	return change, ok
}

func (s *jobState) setTodos(items []TodoItem) {
	s.mu.Lock()
	s.todos = append([]TodoItem(nil), items...)
	s.mu.Unlock()
}

func (s *jobState) setPlan(plan string) {
	s.mu.Lock()
	s.plan = plan
	s.mu.Unlock()
}

func targetKey(scope Scope, name string) string {
	return scope.SessionID + "\x00" + canonicalPath(name)
}

func canonicalPath(name string) string {
	if name == "" {
		return ""
	}
	clean := path.Clean(name)
	if strings.HasPrefix(name, "//") && !strings.HasPrefix(clean, "//") {
		clean = "/" + clean
	}
	return clean
}
