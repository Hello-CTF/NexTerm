package session

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestChannelOwnerRegistrationLifecycle(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { _ = manager.Close() })
	const channelID = "channel-owner-lifecycle"

	if err := manager.CheckChannelOwner(channelID, "alice"); err != nil {
		t.Fatalf("unregistered channel check = %v", err)
	}
	first, err := manager.RegisterChannelOwner(channelID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if first == 0 {
		t.Fatal("initial registration generation is zero")
	}
	if err := manager.CheckChannelOwner(channelID, "alice"); err != nil {
		t.Fatalf("owner check = %v", err)
	}
	if err := manager.CheckChannelOwner(channelID, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner check = %v, want ErrForbidden", err)
	}
	if _, err := manager.RegisterChannelOwner(channelID, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner registration = %v, want ErrForbidden", err)
	}
	owner, generation, err := manager.ChannelOwnerGeneration(channelID)
	if err != nil || owner != "alice" || generation != first {
		t.Fatalf("after rejected registration owner=%q generation=%d err=%v, want alice/%d", owner, generation, err, first)
	}

	second, err := manager.RegisterChannelOwner(channelID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Fatalf("generation did not advance: %d -> %d", first, second)
	}
	manager.UnregisterChannelOwner(channelID, first)
	owner, generation, err = manager.ChannelOwnerGeneration(channelID)
	if err != nil || owner != "alice" || generation != second {
		t.Fatalf("stale unregister removed registration: owner=%q generation=%d err=%v", owner, generation, err)
	}
	if err := manager.CheckChannelOwner(channelID, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner check after generation advance = %v, want ErrForbidden", err)
	}

	manager.UnregisterChannelOwner(channelID, second)
	if _, err := manager.ChannelOwner(channelID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("current unregister left owner behind: %v", err)
	}
	if err := manager.CheckChannelOwner(channelID, "bob"); err != nil {
		t.Fatalf("released channel check = %v", err)
	}
	third, err := manager.RegisterChannelOwner(channelID, "bob")
	if err != nil {
		t.Fatalf("registration after release: %v", err)
	}
	if third <= second {
		t.Fatalf("generation reused after release: %d -> %d", second, third)
	}
	if err := manager.CheckChannelOwner(channelID, "alice"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("previous owner check after release = %v, want ErrForbidden", err)
	}
}

func TestChannelOwnerConcurrentUse(t *testing.T) {
	manager := NewManager(Config{})
	t.Cleanup(func() { _ = manager.Close() })
	const (
		channelID  = "channel-owner-concurrent"
		workers    = 8
		iterations = 100
	)

	start := make(chan struct{})
	generations := make(chan uint64, workers*iterations)
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for iteration := range iterations {
				generation, err := manager.RegisterChannelOwner(channelID, "alice")
				if err != nil {
					results <- fmt.Errorf("worker %d iteration %d register: %w", worker, iteration, err)
					return
				}
				generations <- generation
				if err := manager.CheckChannelOwner(channelID, "alice"); err != nil {
					results <- fmt.Errorf("worker %d iteration %d owner check: %w", worker, iteration, err)
					return
				}
				if err := manager.CheckChannelOwner(channelID, "bob"); !errors.Is(err, ErrForbidden) {
					results <- fmt.Errorf("worker %d iteration %d non-owner check: %w", worker, iteration, err)
					return
				}
				owner, current, err := manager.ChannelOwnerGeneration(channelID)
				if err != nil || owner != "alice" || current < generation {
					results <- fmt.Errorf("worker %d iteration %d generation: owner=%q current=%d registered=%d err=%v", worker, iteration, owner, current, generation, err)
					return
				}
				manager.UnregisterChannelOwner(channelID, 0)
			}
			results <- nil
		}()
	}
	close(start)
	wg.Wait()
	close(generations)

	for range workers {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	seen := make(map[uint64]struct{}, workers*iterations)
	for generation := range generations {
		if _, exists := seen[generation]; exists {
			t.Fatalf("duplicate registration generation %d", generation)
		}
		seen[generation] = struct{}{}
	}
	if err := manager.CheckChannelOwner(channelID, "alice"); err != nil {
		t.Fatalf("final owner check = %v", err)
	}
	if err := manager.CheckChannelOwner(channelID, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("final non-owner check = %v, want ErrForbidden", err)
	}
}
