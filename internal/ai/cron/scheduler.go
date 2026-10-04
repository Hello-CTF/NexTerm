package cron

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

type Scheduler struct {
	store    Store
	executor Executor
	options  Options
	ownerID  string
	errCh    chan error

	mu      sync.Mutex
	running bool
	local   map[string]string
	wg      sync.WaitGroup
}

func NewScheduler(store Store, executor Executor, options Options) (*Scheduler, error) {
	if store == nil {
		return nil, errors.New("cron store is nil")
	}
	if executor == nil {
		return nil, errors.New("cron executor is nil")
	}
	if options.MaxJobsPerSession <= 0 {
		options.MaxJobsPerSession = DefaultMaxJobsPerSession
	}
	if options.ExecutionTimeout <= 0 {
		options.ExecutionTimeout = DefaultExecutionTimeout
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = DefaultLeaseDuration
	}
	if options.PollInterval <= 0 {
		options.PollInterval = DefaultPollInterval
	}
	if options.FailureBackoff <= 0 {
		options.FailureBackoff = DefaultFailureBackoff
	}
	if options.MaxFailureBackoff <= 0 {
		options.MaxFailureBackoff = DefaultMaxFailureBackoff
	}
	if options.MaxFailureBackoff < options.FailureBackoff {
		options.MaxFailureBackoff = options.FailureBackoff
	}
	if options.CircuitThreshold <= 0 {
		options.CircuitThreshold = DefaultCircuitThreshold
	}
	if options.CircuitCooldown <= 0 {
		options.CircuitCooldown = DefaultCircuitCooldown
	}
	if options.NewID == nil {
		options.NewID = ids.New
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	options.OwnerID = strings.TrimSpace(options.OwnerID)
	if options.OwnerID == "" {
		options.OwnerID = "scheduler-" + options.NewID()
	}
	return &Scheduler{
		store: store, executor: executor, options: options, ownerID: options.OwnerID,
		errCh: make(chan error, 1), local: make(map[string]string),
	}, nil
}

func (s *Scheduler) Register(ctx context.Context, registration Registration) (Job, error) {
	registration.SessionID = strings.TrimSpace(registration.SessionID)
	if registration.SessionID == "" {
		return Job{}, errors.New("cron session ID is required")
	}
	if strings.TrimSpace(registration.Prompt) == "" {
		return Job{}, errors.New("cron prompt is required")
	}
	if registration.Timeout < 0 {
		return Job{}, errors.New("cron timeout cannot be negative")
	}
	if registration.Timeout == 0 {
		registration.Timeout = s.options.ExecutionTimeout
	}
	registration.ID = strings.TrimSpace(registration.ID)
	if registration.ID == "" {
		registration.ID = s.options.NewID()
	}
	if registration.ID == "" {
		return Job{}, errors.New("cron generated an empty job ID")
	}
	registration.Timezone = strings.TrimSpace(registration.Timezone)
	if registration.Timezone == "" {
		registration.Timezone = "UTC"
	}
	schedule, err := loadSchedule(registration.Schedule, registration.Timezone)
	if err != nil {
		return Job{}, err
	}
	now := s.now()
	next, ok := schedule.Next(now)
	if !ok {
		return Job{}, fmt.Errorf("%w: no occurrence within ten years", ErrInvalidSchedule)
	}
	job := Job{
		ID: registration.ID, SessionID: registration.SessionID,
		Name: strings.TrimSpace(registration.Name), Prompt: strings.TrimSpace(registration.Prompt),
		Schedule: strings.TrimSpace(registration.Schedule), Timezone: registration.Timezone,
		Enabled: !registration.Disabled, Timeout: registration.Timeout,
		ModelProfileID: strings.TrimSpace(registration.ModelProfileID),
		CreatedAt:      now, UpdatedAt: now, NextRunAt: next,
	}
	stored, err := s.store.Create(ctx, job, s.options.MaxJobsPerSession)
	if err != nil {
		return Job{}, storageError("create", job.ID, err)
	}
	return stored, nil
}

func (s *Scheduler) List(ctx context.Context, sessionID string) ([]Job, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("cron session ID is required")
	}
	jobs, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Job, 0)
	for _, job := range jobs {
		if job.SessionID == sessionID {
			result = append(result, job)
		}
	}
	return result, nil
}

func (s *Scheduler) Get(ctx context.Context, sessionID, jobID string) (Job, error) {
	job, err := s.lookup(ctx, strings.TrimSpace(sessionID), strings.TrimSpace(jobID))
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Scheduler) SetEnabled(ctx context.Context, sessionID, jobID string, enabled bool) (Job, error) {
	job, err := s.lookup(ctx, strings.TrimSpace(sessionID), strings.TrimSpace(jobID))
	if err != nil {
		return Job{}, err
	}
	if job.Running() {
		return Job{}, ErrJobRunning
	}
	if job.Enabled == enabled {
		return job, nil
	}
	now := s.now()
	if enabled {
		schedule, err := loadSchedule(job.Schedule, job.Timezone)
		if err != nil {
			return Job{}, err
		}
		next, ok := schedule.Next(now)
		if !ok {
			return Job{}, fmt.Errorf("%w: no occurrence within ten years", ErrInvalidSchedule)
		}
		job.NextRunAt = next
	}
	job.Enabled = enabled
	job.UpdatedAt = now
	stored, err := s.store.CompareAndSwap(ctx, job, job.Revision)
	if err != nil {
		return Job{}, storageError("set enabled", job.ID, err)
	}
	return stored, nil
}

func (s *Scheduler) Unregister(ctx context.Context, sessionID, jobID string) error {
	job, err := s.lookup(ctx, strings.TrimSpace(sessionID), strings.TrimSpace(jobID))
	if err != nil {
		return err
	}
	if job.Running() {
		return ErrJobRunning
	}
	return storageError("delete", job.ID, s.store.Delete(ctx, job.SessionID, job.ID, job.Revision))
}

func (s *Scheduler) lookup(ctx context.Context, sessionID, jobID string) (Job, error) {
	if sessionID == "" || jobID == "" {
		return Job{}, ErrNotFound
	}
	jobs, err := s.load(ctx)
	if err != nil {
		return Job{}, err
	}
	for _, job := range jobs {
		if job.ID == jobID && job.SessionID == sessionID {
			return job, nil
		}
	}
	return Job{}, ErrNotFound
}

func (s *Scheduler) load(ctx context.Context) ([]Job, error) {
	jobs, err := s.store.List(ctx)
	if err != nil {
		return nil, storageError("list", "", err)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (s *Scheduler) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrAlreadyRunning
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	ticker := time.NewTicker(s.options.PollInterval)
	defer ticker.Stop()
	var result error
	if _, err := s.runOnce(runContext, s.now()); err != nil {
		s.notify(err)
		result = err
		cancel()
	}
loop:
	for result == nil {
		select {
		case <-ctx.Done():
			result = ctx.Err()
			cancel()
		case err := <-s.errCh:
			result = err
			cancel()
		case <-ticker.C:
			if _, err := s.runOnce(runContext, s.now()); err != nil {
				s.notify(err)
				result = err
				cancel()
				break loop
			}
		}
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	for {
		select {
		case err := <-s.errCh:
			result = errors.Join(result, err)
		case <-done:
			for {
				select {
				case err := <-s.errCh:
					result = errors.Join(result, err)
				default:
					return result
				}
			}
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context, now time.Time) (int, error) {
	jobs, err := s.load(ctx)
	if err != nil {
		return 0, err
	}
	started := 0
	for _, job := range jobs {
		if job.Running() {
			if err := s.recoverIfAbandoned(ctx, job, now); err != nil {
				return started, err
			}
			continue
		}
		if !job.Enabled || !due(job, now) {
			continue
		}
		claimed, err := s.claim(ctx, job, now)
		if err != nil {
			return started, err
		}
		if claimed {
			started++
		}
	}
	return started, nil
}

func due(job Job, now time.Time) bool {
	dueAt := job.NextRunAt
	if dueAt.IsZero() {
		dueAt = now
	}
	if job.RetryAt.After(dueAt) {
		dueAt = job.RetryAt
	}
	if job.CircuitOpenUntil.After(dueAt) {
		dueAt = job.CircuitOpenUntil
	}
	return !now.Before(dueAt)
}

func (s *Scheduler) claim(ctx context.Context, job Job, now time.Time) (bool, error) {
	if s.locallyRunning(job.ID, "") || leaseHeldByAnother(job.Lease, s.ownerID, now) {
		return false, nil
	}
	schedule, err := loadSchedule(job.Schedule, job.Timezone)
	if err != nil {
		return false, fmt.Errorf("load schedule for cron job %s: %w", job.ID, err)
	}
	scheduledFor := job.NextRunAt
	if scheduledFor.IsZero() {
		scheduledFor = now
	}
	next, ok := schedule.Next(now)
	if !ok {
		return false, fmt.Errorf("%w: no occurrence within ten years for job %s", ErrInvalidSchedule, job.ID)
	}
	following, ok := schedule.Next(scheduledFor)
	coalesced := ok && !following.After(now)
	runID := s.options.NewID()
	if runID == "" {
		return false, errors.New("cron generated an empty run ID")
	}
	job.Lease = Lease{Owner: s.ownerID, ExpiresAt: now.Add(s.options.LeaseDuration)}
	job.Run = RunState{
		ID: runID, ScheduledFor: scheduledFor, StartedAt: now,
		Deadline: now.Add(job.Timeout), Coalesced: coalesced,
	}
	job.NextRunAt = next
	job.UpdatedAt = now
	stored, err := s.store.CompareAndSwap(ctx, job, job.Revision)
	if err != nil {
		wrapped := storageError("claim", job.ID, err)
		if errors.Is(wrapped, ErrConflict) || errors.Is(wrapped, ErrNotFound) {
			return false, nil
		}
		return false, wrapped
	}
	s.mu.Lock()
	s.local[stored.ID] = stored.Run.ID
	s.wg.Add(1)
	s.mu.Unlock()
	go s.execute(ctx, stored)
	return true, nil
}

func (s *Scheduler) recoverIfAbandoned(ctx context.Context, job Job, now time.Time) error {
	if s.locallyRunning(job.ID, job.Run.ID) || leaseHeldByAnyone(job.Lease, now) || now.Before(job.Run.Deadline) {
		return nil
	}
	job.Lease = Lease{Owner: s.ownerID, ExpiresAt: now.Add(s.options.LeaseDuration)}
	_, err := s.finish(ctx, job, errors.New("execution interrupted before completion"), now)
	if err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func leaseHeldByAnother(lease Lease, owner string, now time.Time) bool {
	return lease.Owner != "" && lease.Owner != owner && now.Before(lease.ExpiresAt)
}

func leaseHeldByAnyone(lease Lease, now time.Time) bool {
	return lease.Owner != "" && now.Before(lease.ExpiresAt)
}

func (s *Scheduler) locallyRunning(jobID, runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.local[jobID]
	return ok && (runID == "" || current == runID)
}

func (s *Scheduler) now() time.Time { return s.options.Now() }

func (s *Scheduler) notify(err error) {
	if err != nil && s.options.OnError != nil {
		s.options.OnError(err)
	}
}

func (s *Scheduler) emit(err error) {
	if err == nil {
		return
	}
	s.notify(err)
	s.errCh <- err
}
