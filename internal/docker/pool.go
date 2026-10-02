package docker

import (
	"context"
	"errors"
	"sync"
)

type GenerationSource interface {
	DockerGeneration(sessionID string) (uint64, error)
}

type GenerationSourceFunc func(sessionID string) (uint64, error)

func (f GenerationSourceFunc) DockerGeneration(sessionID string) (uint64, error) {
	return f(sessionID)
}

type BackendFactory func(ctx context.Context, sessionID string, generation uint64) (Backend, error)

type GenerationalProvider struct {
	generations GenerationSource
	sdkFactory  BackendFactory
	cmdFactory  BackendFactory

	mu      sync.Mutex
	entries map[poolKey]*poolEntry
}

type poolKey struct {
	sessionID string
	kind      string
}

type poolEntry struct {
	mu         sync.Mutex
	generation uint64
	backend    Backend
}

func NewGenerationalProvider(generations GenerationSource, sdkFactory, commandFactory BackendFactory) *GenerationalProvider {
	return &GenerationalProvider{
		generations: generations,
		sdkFactory:  sdkFactory,
		cmdFactory:  commandFactory,
		entries:     make(map[poolKey]*poolEntry),
	}
}

func (p *GenerationalProvider) SDK(ctx context.Context, sessionID string) (Backend, error) {
	if p.sdkFactory == nil {
		return nil, ErrNoBackend
	}
	return p.backend(ctx, sessionID, "sdk", p.sdkFactory)
}

func (p *GenerationalProvider) Command(ctx context.Context, sessionID string) (Backend, error) {
	if p.cmdFactory == nil {
		return nil, ErrNoBackend
	}
	return p.backend(ctx, sessionID, "command", p.cmdFactory)
}

func (p *GenerationalProvider) backend(ctx context.Context, sessionID, kind string, factory BackendFactory) (Backend, error) {
	key := poolKey{sessionID: sessionID, kind: kind}
	p.mu.Lock()
	entry := p.entries[key]
	if entry == nil {
		entry = &poolEntry{}
		p.entries[key] = entry
	}
	p.mu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	for {
		generation, err := p.generations.DockerGeneration(sessionID)
		if err != nil {
			return nil, err
		}
		if entry.backend != nil && entry.generation == generation {
			return entry.backend, nil
		}
		if entry.backend != nil {
			_ = entry.backend.Close()
			entry.backend = nil
		}
		backend, err := factory(ctx, sessionID, generation)
		if err != nil {
			return nil, err
		}
		current, err := p.generations.DockerGeneration(sessionID)
		if err != nil {
			_ = backend.Close()
			return nil, err
		}
		if current != generation {
			_ = backend.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		entry.generation = generation
		entry.backend = backend
		return backend, nil
	}
}

func (p *GenerationalProvider) CloseGeneration(sessionID string, generation uint64) error {
	return p.close(sessionID, &generation)
}

func (p *GenerationalProvider) CloseSession(sessionID string) error {
	return p.close(sessionID, nil)
}

func (p *GenerationalProvider) Close() error {
	p.mu.Lock()
	entries := make([]*poolEntry, 0, len(p.entries))
	seen := make(map[*poolEntry]struct{})
	for _, entry := range p.entries {
		if _, ok := seen[entry]; !ok {
			seen[entry] = struct{}{}
			entries = append(entries, entry)
		}
	}
	p.entries = make(map[poolKey]*poolEntry)
	p.mu.Unlock()
	var result error
	for _, entry := range entries {
		entry.mu.Lock()
		if entry.backend != nil {
			result = errors.Join(result, entry.backend.Close())
			entry.backend = nil
		}
		entry.mu.Unlock()
	}
	return result
}

func (p *GenerationalProvider) close(sessionID string, generation *uint64) error {
	var result error
	for _, kind := range []string{"sdk", "command"} {
		key := poolKey{sessionID: sessionID, kind: kind}
		p.mu.Lock()
		entry := p.entries[key]
		p.mu.Unlock()
		if entry == nil {
			continue
		}
		entry.mu.Lock()
		if entry.backend != nil && (generation == nil || entry.generation == *generation) {
			result = errors.Join(result, entry.backend.Close())
			entry.backend = nil
			p.mu.Lock()
			if p.entries[key] == entry {
				delete(p.entries, key)
			}
			p.mu.Unlock()
		}
		entry.mu.Unlock()
	}
	return result
}
