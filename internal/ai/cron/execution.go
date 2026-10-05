package cron

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *Scheduler) execute(ctx context.Context, job Job) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		if s.local[job.ID] == job.Run.ID {
			delete(s.local, job.ID)
		}
		s.mu.Unlock()
	}()

	runContext, cancel := context.WithTimeout(ctx, job.Timeout)
	defer cancel()
	results := make(chan error, 1)
	trigger := Trigger{
		RunID: job.Run.ID, JobID: job.ID, SessionID: job.SessionID,
		Name: job.Name, Prompt: job.Prompt, ScheduledFor: job.Run.ScheduledFor,
		Coalesced: job.Run.Coalesced, ModelProfileID: job.ModelProfileID,
	}
	go func() {
		results <- s.callExecutor(runContext, trigger)
	}()

	renewInterval := s.options.LeaseDuration / 3
	if renewInterval <= 0 {
		renewInterval = s.options.LeaseDuration
	}
	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-results:
			s.finishExecution(ctx, job, executionError(runContext, job.Timeout, err))
			return
		case <-runContext.Done():
			err := <-results
			s.finishExecution(ctx, job, executionError(runContext, job.Timeout, err))
			return
		case <-ticker.C:
			renewed, err := s.renew(ctx, job)
			if err != nil {
				cancel()
				<-results
				s.reportOwnershipOrStorage(job.ID, err)
				return
			}
			job = renewed
		}
	}
}

func (s *Scheduler) callExecutor(ctx context.Context, trigger Trigger) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("cron executor panic: %v", recovered)
		}
	}()
	return s.executor.Execute(ctx, trigger)
}

func executionError(ctx context.Context, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w after %s", ErrExecutionTimeout, timeout)
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Scheduler) renew(ctx context.Context, job Job) (Job, error) {
	job.Lease = Lease{Owner: s.ownerID, ExpiresAt: s.now().Add(s.options.LeaseDuration)}
	job.UpdatedAt = s.now()
	stored, err := s.store.CompareAndSwap(ctx, job, job.Revision)
	if err != nil {
		return Job{}, storageError("renew lease", job.ID, err)
	}
	return stored, nil
}

func (s *Scheduler) finishExecution(ctx context.Context, job Job, executionErr error) {
	finishContext := context.WithoutCancel(ctx)
	if _, err := s.finish(finishContext, job, executionErr, s.now()); err != nil {
		s.reportOwnershipOrStorage(job.ID, err)
	}
}

func (s *Scheduler) reportOwnershipOrStorage(jobID string, err error) {
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		s.notify(fmt.Errorf("%w: job %s", ErrLeaseLost, jobID))
		return
	}
	s.emit(err)
}

func (s *Scheduler) finish(ctx context.Context, job Job, executionErr error, now time.Time) (Job, error) {
	run := job.Run
	job.Run = RunState{}
	job.Lease = Lease{}
	job.LastRunAt = now
	job.LastScheduledFor = run.ScheduledFor
	job.LastCoalesced = run.Coalesced
	job.UpdatedAt = now
	if executionErr == nil {
		job.LastError = ""
		job.ConsecutiveFailures = 0
		job.RetryAt = time.Time{}
		job.CircuitOpenUntil = time.Time{}
		if OneShot(job.Schedule) {
			job.Enabled = false
			job.NextRunAt = time.Time{}
		}
	} else {
		job.LastError = boundedError(executionErr)
		job.ConsecutiveFailures++
		delay := s.failureDelay(job.ConsecutiveFailures)
		job.RetryAt = now.Add(delay)
		job.CircuitOpenUntil = time.Time{}
		if job.ConsecutiveFailures >= s.options.CircuitThreshold {
			job.CircuitOpenUntil = now.Add(s.options.CircuitCooldown)
			if job.CircuitOpenUntil.After(job.RetryAt) {
				job.RetryAt = job.CircuitOpenUntil
			}
		}
	}
	stored, err := s.store.CompareAndSwap(ctx, job, job.Revision)
	if err != nil {
		return Job{}, storageError("finish", job.ID, err)
	}
	return stored, nil
}

func (s *Scheduler) failureDelay(failures int) time.Duration {
	delay := s.options.FailureBackoff
	maximum := s.options.MaxFailureBackoff
	for failure := 1; failure < failures && delay < maximum; failure++ {
		if delay > maximum/2 {
			delay = maximum
		} else {
			delay *= 2
		}
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func boundedError(err error) string {
	const maximumRunes = 2048
	text := err.Error()
	runes := []rune(text)
	if len(runes) > maximumRunes {
		return string(runes[:maximumRunes])
	}
	return text
}
