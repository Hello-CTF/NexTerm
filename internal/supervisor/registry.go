package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	sessionsDirName = "sessions"
	entryName       = "session.json"
	recordingName   = "output.raw"
	versionsName    = "versions"
)

type registryEntry struct {
	ID          string     `json:"id"`
	CreatedAt   time.Time  `json:"created_at"`
	Incarnation string     `json:"incarnation"`
	Attempt     string     `json:"attempt,omitempty"`
	Cols        uint32     `json:"cols"`
	Rows        uint32     `json:"rows"`
	Command     []string   `json:"command,omitempty"`
	Dir         string     `json:"dir,omitempty"`
	Running     bool       `json:"running"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Signal      string     `json:"signal,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

func (e registryEntry) info() Info {
	info := Info{
		ID:          e.ID,
		CreatedAt:   e.CreatedAt,
		Incarnation: e.Incarnation,
		Cols:        e.Cols,
		Rows:        e.Rows,
		Dead:        !e.Running,
		ExitCode:    e.ExitCode,
		Signal:      e.Signal,
	}
	if e.FinishedAt != nil {
		finished := *e.FinishedAt
		info.FinishedAt = &finished
	}
	return info
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

func recordingPath(stateDir, id string) string {
	return filepath.Join(sessionDir(stateDir, id), recordingName)
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
