package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
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
	createdTabs  map[string]struct{}
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

// rememberCreatedTab records a tab this job opened via terminal_create so
// resolveTab can accept it as an in-scope target for the rest of the job.
func (s *jobState) rememberCreatedTab(tabID string) {
	s.mu.Lock()
	if s.createdTabs == nil {
		s.createdTabs = make(map[string]struct{})
	}
	s.createdTabs[tabID] = struct{}{}
	s.mu.Unlock()
}

func (s *jobState) createdTab(tabID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.createdTabs[tabID]
	return ok
}

func targetKey(scope Scope, name string) string {
	return scope.SessionID + "\x00" + canonicalPath(name)
}

func canonicalPath(name string) string {
	if name == "" {
		return ""
	}
	clean := filepath.Clean(name)
	if strings.HasPrefix(name, "//") && !strings.HasPrefix(filepath.ToSlash(clean), "//") {
		clean = string(filepath.Separator) + clean
	}
	return clean
}
