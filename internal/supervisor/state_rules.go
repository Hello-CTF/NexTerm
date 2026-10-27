package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
)

// stateRulesName is the file in the state directory that keeps the user
// state rules, so a helper restarted without any client online applies them.
const stateRulesName = "state-rules.json"

// StateRuleSet is the ordered set of user state rules a client pushes.
type StateRuleSet = staterule.Set

// StateRuleSource supplies the state rules a client keeps its helper on.
// StateRules returns false when the rules cannot be read now; the helper then
// keeps the rules it has. StateRulesPushed reports the outcome of each push
// (nil on success).
type StateRuleSource interface {
	StateRules() (StateRuleSet, bool)
	StateRulesPushed(revision string, err error)
}

// stateRulesAck is the helper's state rule revision in the hello
// acknowledgement. Its presence tells the client the helper takes
// frameSetStateRules.
type stateRulesAck struct {
	Revision string `json:"revision"`
}

func stateRulesPath(stateDir string) string {
	return filepath.Join(stateDir, stateRulesName)
}

// loadStateRules reads the persisted rules. A missing file means no rules;
// an unreadable or invalid one is reported and leaves the helper without
// rules, with an empty revision, so the next client pushes its rules again.
func loadStateRules(stateDir string) (*staterule.Compiled, error) {
	data, err := os.ReadFile(stateRulesPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state rules: %w", err)
	}
	var set staterule.Set
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("decode state rules: %w", err)
	}
	compiled, err := staterule.Compile(set)
	if err != nil {
		return nil, fmt.Errorf("persisted state rules: %w", err)
	}
	return compiled, nil
}

func saveStateRules(stateDir string, set staterule.Set) error {
	path := stateRulesPath(stateDir)
	if len(set.Rules) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove state rules: %w", err)
		}
		return nil
	}
	data, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("encode state rules: %w", err)
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return fmt.Errorf("write state rules: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("commit state rules: %w", err)
	}
	return nil
}

// StateRules returns the state rules in force.
func (s *Supervisor) StateRules() StateRuleSet {
	return s.stateRules.Load().Set()
}

// StateRulesRevision returns the revision of the state rules in force, empty
// for none.
func (s *Supervisor) StateRulesRevision() string {
	return s.stateRules.Load().Revision()
}

// SetStateRules validates, persists and applies a rule set. Live sessions
// whose output is quiet are evaluated again at once; the others apply the
// rules at their next idle inspection. Invalid rules are rejected whole with
// ErrInvalidInput and the rules in force stay.
func (s *Supervisor) SetStateRules(set StateRuleSet) error {
	compiled, err := staterule.Compile(set)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	s.stateRulesMu.Lock()
	defer s.stateRulesMu.Unlock()
	if compiled.Revision() == s.stateRules.Load().Revision() {
		return nil
	}
	if err := saveStateRules(s.stateDir, compiled.Set()); err != nil {
		return err
	}
	s.stateRules.Store(compiled)
	s.mu.Lock()
	trackers := make([]*stateTracker, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session.state != nil {
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

func (s *Supervisor) currentStateRules() *staterule.Compiled {
	return s.stateRules.Load()
}
