package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

const (
	sessionsDirName = "sessions"
	entryName       = "session.json"
	recordingName   = "output.raw"
	versionsName    = "versions"
)

type renameRecord struct {
	ExpectedMetadataRevision uint64 `json:"expected_metadata_revision"`
	Title                    string `json:"title"`
	ResultTitle              string `json:"result_title"`
	ResultMetadataRevision   uint64 `json:"result_metadata_revision"`
	Conflict                 bool   `json:"conflict,omitempty"`
}

type registryEntry struct {
	ID               string                  `json:"id"`
	CreatedAt        time.Time               `json:"created_at"`
	Incarnation      string                  `json:"incarnation"`
	Attempt          string                  `json:"attempt,omitempty"`
	Title            string                  `json:"title,omitempty"`
	MetadataRevision uint64                  `json:"metadata_revision,omitempty"`
	RenameRequests   map[string]renameRecord `json:"rename_requests,omitempty"`
	Cols             uint32                  `json:"cols"`
	Rows             uint32                  `json:"rows"`
	Command          []string                `json:"command,omitempty"`
	Dir              string                  `json:"dir,omitempty"`
	Running          bool                    `json:"running"`
	ExitCode         *int                    `json:"exit_code,omitempty"`
	Signal           string                  `json:"signal,omitempty"`
	FinishedAt       *time.Time              `json:"finished_at,omitempty"`
	HistoryLimit     uint64                  `json:"history_limit,omitempty"`
}

func (e registryEntry) info() Info {
	info := Info{
		ID:               e.ID,
		CreatedAt:        e.CreatedAt,
		Incarnation:      e.Incarnation,
		Title:            e.Title,
		MetadataRevision: e.MetadataRevision,
		Cols:             e.Cols,
		Rows:             e.Rows,
		Dead:             !e.Running,
		ExitCode:         e.ExitCode,
		Signal:           e.Signal,
	}
	if e.FinishedAt != nil {
		finished := *e.FinishedAt
		info.FinishedAt = &finished
	}
	return info
}

func (s *Session) canonicalInfo() Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info := s.entry.info()
	info.Generation = s.supervisor.generation
	s.historyInfo(&info)
	return info
}

func (s *Supervisor) listCanonical(ctx context.Context) ([]Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session.exec != nil {
			continue
		}
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	infos := make([]Info, 0, len(sessions))
	for _, session := range sessions {
		infos = append(infos, session.canonicalInfo())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos, nil
}

// attachFrom attaches to id. expected, when set, must match the session's
// identity; expectGeneration, when set, must be this helper's generation.
func (s *Supervisor) attachFrom(ctx context.Context, id string, options AttachOptions, expected *Identity, expectGeneration string) (*Attachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.killMu.Lock()
	defer s.killMu.Unlock()
	if err := s.checkGeneration(expectGeneration); err != nil {
		return nil, fmt.Errorf("%w (session %s)", err, id)
	}
	session, err := s.resolveSession(id)
	if err != nil {
		return nil, err
	}
	if expected != nil && !sameIdentity(*expected, session.Identity()) {
		return nil, fmt.Errorf("%w: %s", ErrIdentity, id)
	}
	return session.attach(options)
}

// readHistory reads recorded output of id without attaching; expected is
// required.
func (s *Supervisor) readHistory(ctx context.Context, id string, before uint64, maxBytes int, expected Identity, expectGeneration string) (HistoryChunk, error) {
	if err := ctx.Err(); err != nil {
		return HistoryChunk{}, err
	}
	if err := s.checkGeneration(expectGeneration); err != nil {
		return HistoryChunk{}, fmt.Errorf("%w (session %s)", err, id)
	}
	session, err := s.resolveSession(id)
	if err != nil {
		return HistoryChunk{}, err
	}
	if !sameIdentity(expected, session.Identity()) {
		return HistoryChunk{}, fmt.Errorf("%w: %s", ErrIdentity, id)
	}
	return session.readHistory(before, maxBytes)
}

func (s *Supervisor) Rename(ctx context.Context, request RenameRequest) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	if !ids.Valid(request.RequestID) || !ids.Valid(request.Incarnation) || strings.ContainsRune(request.Title, 0) {
		return Info{}, fmt.Errorf("%w: invalid rename request", ErrInvalidInput)
	}
	s.killMu.Lock()
	defer s.killMu.Unlock()
	session, err := s.resolveSession(request.ID)
	if err != nil {
		return Info{}, err
	}
	if session.Identity().Incarnation != request.Incarnation {
		return Info{}, fmt.Errorf("%w: %s", ErrIdentity, request.ID)
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	current := session.entry.info()
	if record, exists := session.entry.RenameRequests[request.RequestID]; exists {
		if record.ExpectedMetadataRevision != request.ExpectedMetadataRevision || record.Title != request.Title {
			return Info{}, fmt.Errorf("%w: request ID was already used for another rename", ErrInvalidInput)
		}
		current.Title = record.ResultTitle
		current.MetadataRevision = record.ResultMetadataRevision
		if record.Conflict {
			return Info{}, &MetadataConflictError{Current: current}
		}
		return current, nil
	}

	updated := session.entry
	record := renameRecord{
		ExpectedMetadataRevision: request.ExpectedMetadataRevision,
		Title:                    request.Title,
		ResultTitle:              current.Title,
		ResultMetadataRevision:   current.MetadataRevision,
	}
	if request.ExpectedMetadataRevision != current.MetadataRevision {
		record.Conflict = true
		updated = storeRenameRecord(updated, request.RequestID, record)
		if err := writeEntry(sessionsRoot(s.stateDir), updated); err != nil {
			return Info{}, err
		}
		session.entry.Title = updated.Title
		session.entry.MetadataRevision = updated.MetadataRevision
		session.entry.RenameRequests = updated.RenameRequests
		return Info{}, &MetadataConflictError{Current: current}
	}

	updated.Title = request.Title
	updated.MetadataRevision++
	record.ResultTitle = updated.Title
	record.ResultMetadataRevision = updated.MetadataRevision
	updated = storeRenameRecord(updated, request.RequestID, record)
	if err := writeEntry(sessionsRoot(s.stateDir), updated); err != nil {
		return Info{}, err
	}
	session.entry.Title = updated.Title
	session.entry.MetadataRevision = updated.MetadataRevision
	session.entry.RenameRequests = updated.RenameRequests
	return updated.info(), nil
}

func storeRenameRecord(entry registryEntry, requestID string, record renameRecord) registryEntry {
	requests := make(map[string]renameRecord, len(entry.RenameRequests)+1)
	for id, current := range entry.RenameRequests {
		requests[id] = current
	}
	requests[requestID] = record
	entry.RenameRequests = requests
	return entry
}

func sessionsRoot(stateDir string) string {
	return filepath.Join(stateDir, sessionsDirName)
}

func sessionDir(stateDir, id string) string {
	return filepath.Join(sessionsRoot(stateDir), id)
}

func entryPath(stateDir, id string) string {
	return filepath.Join(sessionDir(stateDir, id), entryName)
}

func versionsPath(stateDir, id string) string {
	return filepath.Join(sessionDir(stateDir, id), versionsName)
}

func tombstonePath(stateDir, id string) string {
	return filepath.Join(sessionsRoot(stateDir), id+".delete")
}

func loadRegistry(stateDir string) (map[string]registryEntry, error) {
	root := sessionsRoot(stateDir)
	entries := make(map[string]registryEntry)
	dirents, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read supervisor registry: %w", err)
	}
	for _, dirent := range dirents {
		if !dirent.IsDir() {
			continue
		}
		id := dirent.Name()
		if !ids.Valid(id) {
			continue
		}
		entry, err := readEntry(root, id)
		if errors.Is(err, os.ErrNotExist) {
			if err := removeRegistryArtifacts(root, id); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if entry.Running {
			entry.Running = false
			entry.ExitCode = nil
			entry.Signal = ""
			finished := time.Now()
			entry.FinishedAt = &finished
			if err := writeEntry(root, entry); err != nil {
				return nil, err
			}
		}
		entries[id] = entry
	}
	return entries, nil
}

func readEntry(root, id string) (registryEntry, error) {
	data, err := os.ReadFile(filepath.Join(root, id, entryName))
	if err != nil {
		return registryEntry{}, err
	}
	var entry registryEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return registryEntry{}, fmt.Errorf("parse supervisor registry entry for %s: %w", id, err)
	}
	if entry.ID != id {
		return registryEntry{}, fmt.Errorf("supervisor registry entry ID %q does not match directory %q", entry.ID, id)
	}
	return entry, nil
}

func writeEntry(root string, entry registryEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode supervisor registry entry for %s: %w", entry.ID, err)
	}
	path := filepath.Join(root, entry.ID, entryName)
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return fmt.Errorf("write supervisor registry entry for %s: %w", entry.ID, err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("commit supervisor registry entry for %s: %w", entry.ID, err)
	}
	return nil
}

func removeRegistryArtifacts(root, id string) error {
	if !ids.Valid(id) {
		return fmt.Errorf("%w: invalid supervisor session ID", ErrInvalidInput)
	}
	original := filepath.Join(root, id)
	tombstone := filepath.Join(root, id+".delete")
	if _, err := os.Lstat(tombstone); err == nil {
		if err := os.RemoveAll(tombstone); err != nil {
			return fmt.Errorf("remove prior supervisor tombstone for %s: %w", id, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect supervisor tombstone for %s: %w", id, err)
	}
	if err := os.Rename(original, tombstone); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("quarantine supervisor artifacts for %s: %w", id, err)
	}
	if err := os.RemoveAll(tombstone); err != nil {
		return fmt.Errorf("remove supervisor tombstone for %s: %w", id, err)
	}
	for _, path := range []string{original, tombstone} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("supervisor artifact path remains: %s", id)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("verify supervisor artifact cleanup for %s: %w", id, err)
		}
	}
	return nil
}

func openRecording(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open supervisor recording: %v", ErrUnavailable, err)
	}
	if err := privateRegularFile(info); err != nil {
		return nil, fmt.Errorf("%w: supervisor recording is not a private regular file", ErrUnavailable)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open supervisor recording: %v", ErrUnavailable, err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: supervisor recording changed while opening", ErrUnavailable)
	}
	return file, nil
}

// checkGeneration rejects a request meant for another helper process. An
// empty expectation is accepted only where the protocol allows one.
func (s *Supervisor) checkGeneration(expected string) error {
	if expected != "" && expected != s.generation {
		return fmt.Errorf("%w: expected helper generation %s, this helper is %s", ErrGenerationMismatch, expected, s.generation)
	}
	return nil
}
