package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Component interface {
	Start(context.Context) error
	Shutdown(context.Context) error
}

type ComponentFuncs struct {
	StartFunc    func(context.Context) error
	ShutdownFunc func(context.Context) error
}

func (c ComponentFuncs) Start(ctx context.Context) error {
	if c.StartFunc == nil {
		return nil
	}
	return c.StartFunc(ctx)
}

func (c ComponentFuncs) Shutdown(ctx context.Context) error {
	if c.ShutdownFunc == nil {
		return nil
	}
	return c.ShutdownFunc(ctx)
}

type namedComponent struct {
	name      string
	component Component
}

type Lifecycle struct {
	mu         sync.Mutex
	components []namedComponent
	started    bool
	closed     bool
	cancel     context.CancelFunc
}

func (l *Lifecycle) Add(name string, component Component) error {
	if name == "" {
		return fmt.Errorf("component name is empty")
	}
	if component == nil {
		return fmt.Errorf("component %s is nil", name)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started {
		return fmt.Errorf("lifecycle has already started")
	}
	for _, existing := range l.components {
		if existing.name == name {
			return fmt.Errorf("component %s is already registered", name)
		}
	}
	l.components = append(l.components, namedComponent{name: name, component: component})
	return nil
}

func (l *Lifecycle) Start(ctx context.Context) error {
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return fmt.Errorf("lifecycle has already started")
	}
	l.started = true
	runCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	components := append([]namedComponent(nil), l.components...)
	l.mu.Unlock()

	for index, named := range components {
		if err := named.component.Start(runCtx); err != nil {
			cancel()
			errs := []error{fmt.Errorf("start component %s: %w", named.name, err)}
			for previous := index - 1; previous >= 0; previous-- {
				if closeErr := components[previous].component.Shutdown(context.Background()); closeErr != nil {
					errs = append(errs, fmt.Errorf("rollback component %s: %w", components[previous].name, closeErr))
				}
			}
			l.mu.Lock()
			l.closed = true
			l.mu.Unlock()
			return errors.Join(errs...)
		}
	}
	return nil
}

func (l *Lifecycle) Shutdown(ctx context.Context) error {
	l.mu.Lock()
	if !l.started || l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	if l.cancel != nil {
		l.cancel()
	}
	components := append([]namedComponent(nil), l.components...)
	l.mu.Unlock()

	var errs []error
	for index := len(components) - 1; index >= 0; index-- {
		if err := components[index].component.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown component %s: %w", components[index].name, err))
		}
	}
	return errors.Join(errs...)
}
