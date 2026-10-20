package docker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type DiskCategory struct {
	Kind             string `json:"kind"`
	Total            int    `json:"total"`
	Active           int    `json:"active"`
	SizeBytes        int64  `json:"sizeBytes"`
	ReclaimableBytes int64  `json:"reclaimableBytes"`
}

type DiskImage struct {
	ID              string `json:"id"`
	Repository      string `json:"repository"`
	Tag             string `json:"tag"`
	Containers      int    `json:"containers"`
	SizeBytes       int64  `json:"sizeBytes"`
	UniqueSizeBytes int64  `json:"uniqueSizeBytes"`
}

type DiskContainer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	Status    string `json:"status"`
	SizeBytes int64  `json:"sizeBytes"`
}

type ContainerUse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type VolumeUsage struct {
	Name       string         `json:"name"`
	Driver     string         `json:"driver"`
	Links      int            `json:"links"`
	SizeBytes  int64          `json:"sizeBytes"`
	Containers []ContainerUse `json:"containers"`
}

type NetworkUsage struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Driver     string         `json:"driver"`
	Scope      string         `json:"scope"`
	Builtin    bool           `json:"builtin"`
	Containers []ContainerUse `json:"containers"`
}

type BuildCacheUsage struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	SizeBytes int64  `json:"sizeBytes"`
	Shared    bool   `json:"shared"`
}

type DiskUsage struct {
	Categories []DiskCategory    `json:"categories"`
	Images     []DiskImage       `json:"images"`
	Containers []DiskContainer   `json:"containers"`
	Volumes    []VolumeUsage     `json:"volumes"`
	Networks   []NetworkUsage    `json:"networks"`
	BuildCache []BuildCacheUsage `json:"buildCache"`
}

type ReclaimableProbe struct {
	TotalBytes int64          `json:"totalBytes"`
	Categories []DiskCategory `json:"categories"`
}

type PruneKind string

const (
	PruneKindContainers PruneKind = "containers"
	PruneKindImages     PruneKind = "images"
	PruneKindVolumes    PruneKind = "volumes"
	PruneKindNetworks   PruneKind = "networks"
	PruneKindBuildCache PruneKind = "buildCache"
)

type PruneItem struct {
	Kind  PruneKind `json:"kind"`
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Bytes int64     `json:"bytes"`
}

type PrunePreview struct {
	Token      string      `json:"token"`
	Digest     string      `json:"digest"`
	ExpiresAt  int64       `json:"expiresAt"`
	Items      []PruneItem `json:"items"`
	TotalBytes int64       `json:"totalBytes"`
}

type PruneRequest struct {
	SessionID string
	AssetID   string
	Source    string
	Token     string
	Digest    string
}

type PruneItemResult struct {
	Item   PruneItem `json:"item"`
	Output string    `json:"output,omitempty"`
	Error  string    `json:"error,omitempty"`
}

type PruneResult struct {
	Items      []PruneItemResult `json:"items"`
	FreedBytes int64             `json:"freedBytes"`
}

type DiskCommandBackend interface {
	SystemDF(ctx context.Context) (string, error)
	DiskUsage(ctx context.Context) (DiskUsage, error)
	ReclaimableBuildCache(ctx context.Context) ([]PruneItem, error)
	RemovePruneItem(ctx context.Context, item PruneItem) (string, error)
}

type storedPrunePreview struct {
	sessionID string
	preview   PrunePreview
}

func (s *Service) diskCommandBackend(ctx context.Context, sessionID string) (DiskCommandBackend, error) {
	backend, err := s.provider.Command(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	command, ok := backend.(DiskCommandBackend)
	if !ok {
		return nil, fmt.Errorf("%w: disk command backend", ErrUnsupported)
	}
	return command, nil
}

func (s *Service) Reclaimable(ctx context.Context, sessionID string) (ReclaimableProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	backend, err := s.diskCommandBackend(ctx, sessionID)
	if err != nil {
		return ReclaimableProbe{}, err
	}
	output, err := backend.SystemDF(ctx)
	if err != nil {
		return ReclaimableProbe{}, err
	}
	usage, err := parseDockerDF(output)
	if err != nil {
		return ReclaimableProbe{}, err
	}
	probe := ReclaimableProbe{Categories: usage.Categories}
	for _, category := range usage.Categories {
		probe.TotalBytes += category.ReclaimableBytes
	}
	return probe, nil
}

func (s *Service) DiskUsage(ctx context.Context, sessionID string) (DiskUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	backend, err := s.diskCommandBackend(ctx, sessionID)
	if err != nil {
		return DiskUsage{}, err
	}
	return backend.DiskUsage(ctx)
}

func (s *Service) PreviewPrune(ctx context.Context, sessionID string, kinds []PruneKind) (PrunePreview, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	if len(kinds) == 0 {
		return PrunePreview{}, errors.New("at least one prune kind is required")
	}
	selected, err := normalizePruneKinds(kinds)
	if err != nil {
		return PrunePreview{}, err
	}
	backend, err := s.diskCommandBackend(ctx, sessionID)
	if err != nil {
		return PrunePreview{}, err
	}
	usage, err := backend.DiskUsage(ctx)
	if err != nil {
		return PrunePreview{}, err
	}
	items := make([]PruneItem, 0)
	for kind := range selected {
		switch kind {
		case PruneKindContainers:
			for _, item := range usage.Containers {
				status := strings.ToLower(item.Status)
				if strings.HasPrefix(status, "exited") || strings.HasPrefix(status, "created") || strings.HasPrefix(status, "dead") {
					items = append(items, PruneItem{Kind: kind, ID: item.ID, Name: item.Name, Bytes: item.SizeBytes})
				}
			}
		case PruneKindImages:
			for _, item := range usage.Images {
				if item.Containers == 0 && (item.Repository == noneLabel || item.Tag == noneLabel) {
					items = append(items, PruneItem{Kind: kind, ID: item.ID, Name: item.Repository + ":" + item.Tag, Bytes: item.UniqueSizeBytes})
				}
			}
		case PruneKindVolumes:
			for _, item := range usage.Volumes {
				if item.Links == 0 && len(item.Containers) == 0 {
					items = append(items, PruneItem{Kind: kind, ID: item.Name, Name: item.Name, Bytes: item.SizeBytes})
				}
			}
		case PruneKindNetworks:
			for _, item := range usage.Networks {
				if !item.Builtin && len(item.Containers) == 0 {
					items = append(items, PruneItem{Kind: kind, ID: item.Name, Name: item.Name})
				}
			}
		case PruneKindBuildCache:
			cacheItems, cacheErr := backend.ReclaimableBuildCache(ctx)
			if cacheErr != nil {
				return PrunePreview{}, cacheErr
			}
			items = append(items, cacheItems...)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return PrunePreview{}, err
	}
	preview := PrunePreview{
		Token: hex.EncodeToString(tokenBytes[:]), Digest: pruneItemsDigest(items),
		ExpiresAt: s.config.Now().Add(5 * time.Minute).UnixMilli(), Items: items,
	}
	for _, item := range items {
		preview.TotalBytes += item.Bytes
	}
	s.pruneMu.Lock()
	if s.prunePreviews == nil {
		s.prunePreviews = make(map[string]storedPrunePreview)
	}
	now := s.config.Now()
	for token, stored := range s.prunePreviews {
		if stored.sessionID == sessionID && stored.preview.ExpiresAt <= now.UnixMilli() {
			delete(s.prunePreviews, token)
		}
	}
	s.prunePreviews[preview.Token] = storedPrunePreview{sessionID: sessionID, preview: preview}
	s.pruneMu.Unlock()
	return preview, nil
}

func normalizePruneKinds(kinds []PruneKind) (map[PruneKind]struct{}, error) {
	selected := make(map[PruneKind]struct{}, len(kinds))
	for _, kind := range kinds {
		switch kind {
		case PruneKindContainers, PruneKindImages, PruneKindVolumes, PruneKindNetworks, PruneKindBuildCache:
			selected[kind] = struct{}{}
		default:
			return nil, fmt.Errorf("%w: prune kind %s", ErrInvalidAction, kind)
		}
	}
	return selected, nil
}

func pruneItemsDigest(items []PruneItem) string {
	canonical, _ := json.Marshal(items)
	sum := sha256.Sum256(append([]byte("nexterm.docker.prune-v1\x00"), canonical...))
	return hex.EncodeToString(sum[:])
}

func (s *Service) Prune(ctx context.Context, request PruneRequest) (PruneResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.PullTimeout)
	defer cancel()
	s.pruneMu.Lock()
	stored, ok := s.prunePreviews[request.Token]
	s.pruneMu.Unlock()
	if !ok || stored.sessionID != request.SessionID {
		return PruneResult{}, errors.New("prune preview is missing or already used; refresh the preview")
	}
	if stored.preview.ExpiresAt <= s.config.Now().UnixMilli() || stored.preview.Digest != request.Digest {
		return PruneResult{}, errors.New("prune preview expired or does not match; refresh the preview")
	}
	backend, err := s.diskCommandBackend(ctx, request.SessionID)
	if err != nil {
		return PruneResult{}, err
	}
	s.pruneMu.Lock()
	if current, exists := s.prunePreviews[request.Token]; !exists || current.preview.Digest != stored.preview.Digest {
		s.pruneMu.Unlock()
		return PruneResult{}, errors.New("prune preview is already used; refresh the preview")
	}
	delete(s.prunePreviews, request.Token)
	s.pruneMu.Unlock()
	started := s.config.Now()
	result := PruneResult{Items: make([]PruneItemResult, 0, len(stored.preview.Items))}
	exitCode := 0
	for _, item := range stored.preview.Items {
		if ctx.Err() != nil {
			result.Items = append(result.Items, PruneItemResult{Item: item, Error: ctx.Err().Error()})
			exitCode = 1
			continue
		}
		output, removeErr := backend.RemovePruneItem(ctx, item)
		entry := PruneItemResult{Item: item, Output: output}
		if removeErr != nil {
			entry.Error = removeErr.Error()
			exitCode = 1
		} else {
			result.FreedBytes += item.Bytes
		}
		result.Items = append(result.Items, entry)
	}
	s.recordAudit(context.WithoutCancel(ctx), AuditEvent{
		SessionID: request.SessionID, AssetID: request.AssetID, Source: auditSource(request.Source),
		Kind: "docker_prune", Payload: map[string]any{"items": len(result.Items), "freedBytes": result.FreedBytes},
		ExitCode: exitCode, Duration: s.config.Now().Sub(started),
	})
	return result, nil
}

func (s *Service) clearPrunePreviews(sessionID string) {
	s.pruneMu.Lock()
	for token, stored := range s.prunePreviews {
		if stored.sessionID == sessionID {
			delete(s.prunePreviews, token)
		}
	}
	s.pruneMu.Unlock()
}
