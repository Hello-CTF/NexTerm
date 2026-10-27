package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
)

// State rules belong to rule owners. A client that acts for an owner sends
// the owner's opaque key in its hello (helloMsg.StateRulesOwner); the
// sessions it creates are evaluated with that owner's rules only, and its
// frameSetStateRules replaces only that owner's set. So clients of different
// devices, accounts or assets sharing one helper never overwrite each other's
// rules. Sessions created without a key, or before the helper knew about
// owners, use no rules.
//
// Each owner's set is kept in its own file under the state directory, so a
// helper restarted without any client online applies them. At most
// maxStateRuleOwners sets are kept; a new owner evicts the one least
// recently pushed or confirmed in a hello.
const (
	stateRulesDirName  = "state-rules"
	maxStateRuleOwners = 16
	// stateRulesOwnerLen is the length of an owner key: hexadecimal.
	stateRulesOwnerLen = 32
)

// StateRuleSet is the ordered set of user state rules a client pushes.
type StateRuleSet = staterule.Set

// StateRuleSource supplies the state rules a client keeps its helper on.
// StateRulesOwner returns the opaque owner key the client acts for.
// StateRules returns false when the rules cannot be read now; the helper then
// keeps the owner's rules as they are. A set without rules is pushed only to
// clear rules the helper still holds for the owner. StateRulesPushed reports
// the outcome of each push or confirmation (nil on success).
type StateRuleSource interface {
	StateRulesOwner() string
	StateRules() (StateRuleSet, bool)
	StateRulesPushed(revision string, err error)
}

// stateRulesAck is the owner's state rule revision in the hello
// acknowledgement. Its presence tells the client the helper takes
// frameSetStateRules.
type stateRulesAck struct {
	Revision string `json:"revision"`
}

// validStateRulesOwner reports whether key is a well-formed owner key: 32
// lowercase hexadecimal characters, which also makes it a safe file name.
func validStateRulesOwner(key string) bool {
	if len(key) != stateRulesOwnerLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// stateRuleOwners holds the rule sets of all owners.
type stateRuleOwners struct {
	dir string

	// mu guards sets; writeMu orders updates and their files.
	mu      sync.RWMutex
	sets    map[string]*ownerRules
	writeMu sync.Mutex
}

type ownerRules struct {
	compiled *staterule.Compiled
	// used is when the set was last pushed or confirmed by its owner.
	used time.Time
}

// ownerRulesFile is the persisted form of one owner's set.
type ownerRulesFile struct {
	PushedAt int64        `json:"pushed_at"`
	Set      StateRuleSet `json:"set"`
}

func stateRulesDir(stateDir string) string {
	return filepath.Join(stateDir, stateRulesDirName)
}

// loadStateRuleOwners reads the persisted sets. A set that cannot be read
// is logged and skipped: its owner's sessions use no rules and the owner's
// next client pushes the set again, as the helper reports no revision.
func loadStateRuleOwners(stateDir string) *stateRuleOwners {
	owners := &stateRuleOwners{dir: stateRulesDir(stateDir), sets: make(map[string]*ownerRules)}
	entries, err := os.ReadDir(owners.dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("read the state rule directory; helper starts without state rules", "error", err)
		}
		return owners
	}
	for _, entry := range entries {
		key, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !validStateRulesOwner(key) {
			continue
		}
		compiled, pushedAt, err := readOwnerRules(filepath.Join(owners.dir, entry.Name()))
		if err != nil {
			slog.Warn("state rules file is unreadable; its owner uses no rules until it pushes them again", "file", entry.Name(), "error", err)
			continue
		}
		if compiled != nil {
			owners.sets[key] = &ownerRules{compiled: compiled, used: pushedAt}
		}
	}
	return owners
}

func readOwnerRules(path string) (*staterule.Compiled, time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	var file ownerRulesFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode: %w", err)
	}
	compiled, err := staterule.Compile(file.Set)
	if err != nil {
		return nil, time.Time{}, err
	}
	return compiled, time.UnixMilli(file.PushedAt), nil
}

// writeOwnerRules persists a set durably: the data is synced before the
// rename, and a failed write leaves no temporary file behind.
func writeOwnerRules(dir, key string, file ownerRulesFile) (err error) {
	if err := ensurePrivateDir(dir); err != nil {
		return fmt.Errorf("state rule directory: %w", err)
	}
	data, err := json.Marshal(file)
	if err != nil {
		return fmt.Errorf("encode state rules: %w", err)
	}
	path := filepath.Join(dir, key+".json")
	temp := path + ".tmp"
	out, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write state rules: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(temp)
		}
	}()
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		return fmt.Errorf("write state rules: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("sync state rules: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close state rules: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("commit state rules: %w", err)
	}
	return nil
}

func removeOwnerRules(dir, key string) error {
	if err := os.Remove(filepath.Join(dir, key+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove state rules: %w", err)
	}
	return nil
}

// compiled returns the owner's rules; nil for none or no owner.
func (o *stateRuleOwners) compiled(key string) *staterule.Compiled {
	if key == "" {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if set := o.sets[key]; set != nil {
		return set.compiled
	}
	return nil
}

// seen returns the owner's revision and counts the hello as a use.
func (o *stateRuleOwners) seen(key string, now time.Time) string {
	if key == "" {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	set := o.sets[key]
	if set == nil {
		return ""
	}
	set.used = now
	return set.compiled.Revision()
}

// set validates, persists and installs the owner's set, evicting the least
// recently used owner beyond the limit. It returns whether the set changed
// and the owners whose rules went away.
func (o *stateRuleOwners) set(key string, set StateRuleSet, now time.Time) (changed bool, evicted []string, err error) {
	if !validStateRulesOwner(key) {
		return false, nil, fmt.Errorf("%w: malformed state rule owner key", ErrInvalidInput)
	}
	compiled, err := staterule.Compile(set)
	if err != nil {
		return false, nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	o.writeMu.Lock()
	defer o.writeMu.Unlock()
	o.mu.RLock()
	current := o.sets[key]
	o.mu.RUnlock()
	if current != nil && current.compiled.Revision() == compiled.Revision() {
		o.mu.Lock()
		current.used = now
		o.mu.Unlock()
		return false, nil, nil
	}
	if compiled == nil {
		if current == nil {
			return false, nil, nil
		}
		if err := removeOwnerRules(o.dir, key); err != nil {
			return false, nil, err
		}
		o.mu.Lock()
		delete(o.sets, key)
		o.mu.Unlock()
		return true, nil, nil
	}
	if current == nil {
		evicted = o.evictionsLocked()
	}
	if err := writeOwnerRules(o.dir, key, ownerRulesFile{PushedAt: now.UnixMilli(), Set: compiled.Set()}); err != nil {
		return false, nil, err
	}
	for _, old := range evicted {
		if err := removeOwnerRules(o.dir, old); err != nil {
			slog.Warn("remove an evicted owner's state rules", "error", err)
		}
	}
	o.mu.Lock()
	for _, old := range evicted {
		delete(o.sets, old)
	}
	o.sets[key] = &ownerRules{compiled: compiled, used: now}
	o.mu.Unlock()
	return true, evicted, nil
}

// evictionsLocked picks the owners to drop so a new one fits, least recently
// used first. The caller holds writeMu.
func (o *stateRuleOwners) evictionsLocked() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	excess := len(o.sets) + 1 - maxStateRuleOwners
	if excess <= 0 {
		return nil
	}
	keys := make([]string, 0, len(o.sets))
	for key := range o.sets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := o.sets[keys[i]].used, o.sets[keys[j]].used
		if !a.Equal(b) {
			return a.Before(b)
		}
		return keys[i] < keys[j]
	})
	return keys[:excess]
}

// StateRules returns the state rules of an owner.
func (s *Supervisor) StateRules(owner string) StateRuleSet {
	return s.stateRules.compiled(owner).Set()
}

// StateRulesRevision returns the revision of an owner's state rules, empty
// for none.
func (s *Supervisor) StateRulesRevision(owner string) string {
	return s.stateRules.compiled(owner).Revision()
}

func (s *Supervisor) stateRulesSeen(owner string) string {
	return s.stateRules.seen(owner, s.now())
}

// SetStateRules validates, persists and applies an owner's rule set. That
// owner's live sessions whose output is quiet are evaluated again at once;
// the others apply the rules at their next idle inspection. Invalid rules
// are rejected whole with ErrInvalidInput and the owner's rules stay.
func (s *Supervisor) SetStateRules(owner string, set StateRuleSet) error {
	changed, evicted, err := s.stateRules.set(owner, set, s.now())
	if err != nil || !changed {
		return err
	}
	affected := map[string]bool{owner: true}
	for _, key := range evicted {
		affected[key] = true
	}
	s.mu.Lock()
	trackers := make([]*stateTracker, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session.state != nil && affected[session.state.opts.rulesOwner] {
			trackers = append(trackers, session.state)
		}
	}
	s.mu.Unlock()
	now := time.Now()
	for _, tracker := range trackers {
		tracker.rulesChanged(now)
	}
	return nil
}
