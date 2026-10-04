//go:build unix

package supervisor

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func awaitHelperStop(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		defer close(done)
		select {
		case <-signals:
		case <-ctx.Done():
		}
		signal.Stop(signals)
	}()
	return done
}
