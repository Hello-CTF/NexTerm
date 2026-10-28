package localhelper

import (
	"testing"
	"time"
)

// A helper that died must stop being offered as ready at once, even while the
// service backs off before its next attempt; otherwise terminals keep being
// attached to a provider whose helper is gone.
func TestServiceWithdrawsTheProviderOfAnExitedHelperDuringBackoff(t *testing.T) {
	world := newFakeWorld()
	config := world.config(t)
	config.Backoff = []time.Duration{time.Hour}
	config.BreakerThreshold = 5
	service := startService(t, config)
	ready(t, service)
	helper := <-world.connected
	close(helper.exited)
	deadline := time.Now().Add(5 * time.Second)
	for service.Status().State == StateReady {
		if time.Now().After(deadline) {
			t.Fatal("an exited helper is still reported ready during the respawn backoff")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := service.Provider(); err == nil {
		t.Fatal("Provider returned the provider of an exited helper")
	}
}
