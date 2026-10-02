package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	DefaultBlobTTL           = 2 * time.Hour
	DefaultBlobSweepInterval = 10 * time.Minute
	blobCopyBuffer           = 256 << 10
)

type BlobStore struct {
	dataDir    string
	ttl        time.Duration
	sweepEvery time.Duration
	newID      func() string
	now        func() time.Time
	logger     *slog.Logger
}

type stagedBlob struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Bytes *int64 `json:"bytes,omitempty"`
}

func NewBlobStore(dataDir string, logger *slog.Logger) *BlobStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &BlobStore{
		dataDir: dataDir, ttl: DefaultBlobTTL, sweepEvery: DefaultBlobSweepInterval,
		newID: ids.New, now: time.Now, logger: logger,
	}
}

func (b *BlobStore) stageRoot() string { return filepath.Join(b.dataDir, "blobs") }
func (b *BlobStore) keepRoot() string  { return filepath.Join(b.dataDir, "files") }

func (b *BlobStore) Stage(w http.ResponseWriter, r *http.Request) {
	persist, err := optionalBool(r, "persist")
	if err != nil {
		http.Error(w, "invalid persist value", http.StatusBadRequest)
		return
	}
	root := b.stageRoot()
	if persist {
		root = b.keepRoot()
	}
	dir, id, err := b.createItemDir(root)
	if err != nil {
		http.Error(w, "staging directory unavailable", http.StatusInternalServerError)
		return
	}
	path := filepath.Join(dir, SafeBlobName(r.URL.Query().Get("name")))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "staging file unavailable", http.StatusInternalServerError)
		return
	}
	written, copyErr := io.CopyBuffer(file, r.Body, make([]byte, blobCopyBuffer))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "staging write failed", http.StatusInternalServerError)
		return
	}
	writeBlobJSON(w, stagedBlob{ID: id, Path: path, Bytes: &written})
}

func (b *BlobStore) Reserve(w http.ResponseWriter, r *http.Request) {
	dir, id, err := b.createItemDir(b.stageRoot())
	if err != nil {
		http.Error(w, "staging directory unavailable", http.StatusInternalServerError)
		return
	}
	path := filepath.Join(dir, SafeBlobName(r.URL.Query().Get("name")))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "reservation failed", http.StatusInternalServerError)
		return
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "reservation failed", http.StatusInternalServerError)
		return
	}
	writeBlobJSON(w, stagedBlob{ID: id, Path: path})
}

func (b *BlobStore) Download(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !ids.Valid(id) {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	path, err := findBlob(b.stageRoot(), id)
	if err != nil {
		http.Error(w, "staged item not found or expired", http.StatusNotFound)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "staged item is not readable", http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "staged item is not readable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", blobContentDisposition(filepath.Base(path)))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func (b *BlobStore) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !ids.Valid(id) {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(b.stageRoot(), id)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "staged item not found or expired", http.StatusNotFound)
		return
	}
	if err != nil || !info.IsDir() {
		http.Error(w, "staged item not found or expired", http.StatusNotFound)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *BlobStore) createItemDir(root string) (string, string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	for range 3 {
		id := b.newID()
		if !ids.Valid(id) {
			return "", "", fmt.Errorf("invalid generated blob id")
		}
		dir := filepath.Join(root, id)
		err := os.Mkdir(dir, 0o700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return dir, id, err
	}
	return "", "", fmt.Errorf("blob id collision")
}

func SafeBlobName(raw string) string {
	if index := strings.LastIndexAny(raw, `/\`); index >= 0 {
		raw = raw[index+1:]
	}
	raw = strings.TrimSpace(raw)
	cleaned := strings.Map(func(char rune) rune {
		if char == 0 || unicode.IsControl(char) {
			return '_'
		}
		return char
	}, raw)
	cleaned = strings.TrimSpace(strings.TrimLeft(cleaned, "."))
	if cleaned == "" {
		return "blob"
	}
	runes := []rune(cleaned)
	if len(runes) > 120 {
		runes = runes[:120]
	}
	for len(string(runes)) > 240 {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

func findBlob(root, id string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, id))
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			return filepath.Join(root, id, entry.Name()), nil
		}
	}
	return "", os.ErrNotExist
}

func (b *BlobStore) SweepOnce(ctx context.Context) (int, error) {
	root := b.stageRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	now := b.now()
	removed := 0
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		info, err := entry.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		age := now.Sub(info.ModTime())
		if age < 0 || age <= b.ttl {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func (b *BlobStore) RunSweeper(ctx context.Context) {
	interval := b.sweepEvery
	if interval <= 0 {
		interval = DefaultBlobSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed, err := b.SweepOnce(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				b.logger.Warn("blob sweep failed", "error", err)
			}
			if removed > 0 {
				b.logger.Info("expired staged blobs removed", "count", removed)
			}
		}
	}
}

func optionalBool(r *http.Request, name string) (bool, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

func writeBlobJSON(w http.ResponseWriter, blob stagedBlob) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		OK   bool       `json:"ok"`
		Data stagedBlob `json:"data"`
	}{OK: true, Data: blob})
}

func blobContentDisposition(name string) string {
	var encoded strings.Builder
	for _, value := range []byte(name) {
		if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value)) {
			encoded.WriteByte(value)
		} else {
			_, _ = fmt.Fprintf(&encoded, "%%%02X", value)
		}
	}
	return "attachment; filename*=UTF-8''" + encoded.String()
}
