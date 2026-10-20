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
	createdTabs  map[string]string
	lastInput    map[string]terminalInputState
	// assetOperations maps a call ID to the asset identities it executed
	// under and the session its explicit connect established: the stable
	// operation→identity mapping a later retry adjudicates before
	// reconnecting.
	assetOperations map[string]assetOperationBinding
}

// assetOperationBinding is the stable operation→identity mapping entry for
// one recorded call.
type assetOperationBinding struct {
	identities []AssetIdentity
	// sessionID is the session the identities were bound from, re-read to
	// prove reconfiguration before a finished record replays.
	sessionID string
	// connectedSessionID is set only when this call's explicit connect
	// established the session, so the replay names the residual.
	connectedSessionID string
}

// terminalInputState is the terminal input buffer the job last observed via
// read_screen. send_keys refuses to write when the live buffer no longer
// matches this baseline — new user input arrived after the AI last looked —
// unless the call forces through with full knowledge.
type terminalInputState struct {
	buffered string
	cursor   int
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

// rememberCreatedTab records the session binding this job opened so
// resolveTab can accept the tab without widening access to other tabs.
func (s *jobState) rememberCreatedTab(tabID, sessionID string) {
	s.mu.Lock()
	if s.createdTabs == nil {
		s.createdTabs = make(map[string]string)
	}
	s.createdTabs[tabID] = sessionID
	s.mu.Unlock()
}

func (s *jobState) createdTab(tabID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID, ok := s.createdTabs[tabID]
	return sessionID, ok
}

// observeInput records the terminal input buffer the job actually saw, so
// later writes can detect new user input. Only read_screen, post-write
// prediction, and the confirmed-resume sync (the user approved against
// exactly this buffer) call this: wait tails and the Eino classification
// snapshot never surface the input buffer as observed state, so they must
// not refresh the baseline.
func (s *jobState) observeInput(tabID, buffered string, cursor int) {
	s.mu.Lock()
	if s.lastInput == nil {
		s.lastInput = make(map[string]terminalInputState)
	}
	s.lastInput[tabID] = terminalInputState{buffered: buffered, cursor: cursor}
	s.mu.Unlock()
}

// inputChanged reports whether the live terminal input differs from the
// baseline this job observed, and whether a baseline exists at all.
func (s *jobState) inputChanged(tabID, buffered string, cursor int) (changed bool, observed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	baseline, ok := s.lastInput[tabID]
	if !ok {
		return false, false
	}
	return baseline.buffered != buffered || baseline.cursor != cursor, true
}

// rememberAssetOperation records the identities a call bound, the session
// they were bound from, and the session its explicit connect established, so
// a later retry can adjudicate the operation's outcome records before
// reconnecting.
func (s *jobState) rememberAssetOperation(callID string, identities []AssetIdentity, sessionID, connectedSessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assetOperations == nil {
		s.assetOperations = make(map[string]assetOperationBinding)
	}
	binding := s.assetOperations[callID]
	for _, identity := range identities {
		known := false
		for _, existing := range binding.identities {
			if existing == identity {
				known = true
				break
			}
		}
		if !known {
			binding.identities = append(binding.identities, identity)
		}
	}
	if sessionID != "" {
		binding.sessionID = sessionID
	}
	if connectedSessionID != "" {
		binding.connectedSessionID = connectedSessionID
	}
	s.assetOperations[callID] = binding
}

func (s *jobState) assetOperation(callID string) (assetOperationBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.assetOperations[callID]
	return binding, ok
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
