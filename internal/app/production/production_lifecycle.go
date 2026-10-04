package production

import (
	"context"
	"errors"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
	"github.com/ProbiusOfficial/NexTerm/internal/tasks"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func closeStoreComponent(store *store.Store) Component {
	return ComponentFuncs{ShutdownFunc: func(context.Context) error {
		return store.Close()
	}}
}

func closeTasksComponent(manager *tasks.Manager) Component {
	return ComponentFuncs{ShutdownFunc: func(ctx context.Context) error {
		return manager.Close(ctx)
	}}
}

func closeDockerComponent(service *docker.Service) Component {
	return ComponentFuncs{ShutdownFunc: func(context.Context) error {
		return service.Close()
	}}
}

func closeSupervisorComponent(instance *supervisor.Supervisor) Component {
	return ComponentFuncs{ShutdownFunc: func(context.Context) error {
		return instance.Close()
	}}
}

type vaultComponent struct {
	vault *vault.Vault

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newVaultComponent(credentialVault *vault.Vault) Component {
	return &vaultComponent{vault: credentialVault}
}

func (c *vaultComponent) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.done = make(chan struct{})
	done := c.done
	go func() {
		defer close(done)
		c.vault.RunAutoLock(runCtx)
	}()
	return nil
}

func (c *vaultComponent) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.cancel = nil
	c.done = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	var err error
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	c.vault.Lock()
	return errors.Join(err)
}
