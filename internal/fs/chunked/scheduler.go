package chunked

import (
	"context"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Target is one directory on a destination whose chunked-upload staging
// should be reaped once abandoned.
type Target struct {
	FileSystem base.FileSystem
	Dir        string
}

// Scheduler periodically reaps chunked-upload staging left behind by crashed
// sessions or aborted uploads. Each sweep runs ReapStaleStaging per target
// with a shared minimum age, so in-flight uploads are never reaped.
type Scheduler struct {
	interval time.Duration
	minAge   time.Duration
	targets  func(context.Context) ([]Target, error)
	onError  func(error)
}

func NewScheduler(interval, minAge time.Duration, targets func(context.Context) ([]Target, error), onError func(error)) *Scheduler {
	if interval <= 0 {
		interval = time.Hour
	}
	if minAge < 0 {
		minAge = 0
	}
	return &Scheduler{interval: interval, minAge: minAge, targets: targets, onError: onError}
}

// Sweep reaps every target once and returns the number of removed staging
// files. A failing target is reported and skipped; remaining targets proceed.
// When enumeration itself fails partially, the resolved targets still sweep
// and the enumeration error is returned alongside the removal count.
func (s *Scheduler) Sweep(ctx context.Context) (int, error) {
	targets, err := s.targets(ctx)
	removed := 0
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		report, err := ReapStaleStaging(ctx, target.FileSystem, target.Dir, s.minAge)
		if err != nil {
			if s.onError != nil {
				s.onError(err)
			}
			continue
		}
		removed += len(report.Removed)
	}
	return removed, err
}

// Run sweeps immediately and then on every interval until ctx is canceled.
func (s *Scheduler) Run(ctx context.Context) {
	s.sweepAndReport(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepAndReport(ctx)
		}
	}
}

func (s *Scheduler) sweepAndReport(ctx context.Context) {
	if _, err := s.Sweep(ctx); err != nil && s.onError != nil {
		s.onError(err)
	}
}
