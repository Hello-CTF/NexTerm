package cron

import (
	"context"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu       sync.Mutex
	jobs     map[string]Job
	failures map[string]error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{jobs: make(map[string]Job), failures: make(map[string]error)}
}

func (s *memoryStore) List(ctx context.Context) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failures["list"]; err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *memoryStore) Create(ctx context.Context, job Job, maxPerSession int) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failures["create"]; err != nil {
		return Job{}, err
	}
	if _, exists := s.jobs[job.ID]; exists {
		return Job{}, ErrConflict
	}
	count := 0
	for _, existing := range s.jobs {
		if existing.SessionID == job.SessionID {
			count++
		}
	}
	if count >= maxPerSession {
		return Job{}, ErrJobLimit
	}
	job.Revision = 1
	s.jobs[job.ID] = job
	return job, nil
}

func (s *memoryStore) CompareAndSwap(ctx context.Context, job Job, expectedRevision uint64) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failures["cas"]; err != nil {
		return Job{}, err
	}
	current, exists := s.jobs[job.ID]
	if !exists {
		return Job{}, ErrNotFound
	}
	if current.Revision != expectedRevision || current.SessionID != job.SessionID {
		return Job{}, ErrConflict
	}
	job.Revision = current.Revision + 1
	s.jobs[job.ID] = job
	return job, nil
}

func (s *memoryStore) Delete(ctx context.Context, sessionID, jobID string, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failures["delete"]; err != nil {
		return err
	}
	current, exists := s.jobs[jobID]
	if !exists || current.SessionID != sessionID {
		return ErrNotFound
	}
	if current.Revision != expectedRevision {
		return ErrConflict
	}
	delete(s.jobs, jobID)
	return nil
}

func (s *memoryStore) fail(op string, err error) {
	s.mu.Lock()
	s.failures[op] = err
	s.mu.Unlock()
}

func (s *memoryStore) heal(op string) {
	s.mu.Lock()
	delete(s.failures, op)
	s.mu.Unlock()
}

func (s *memoryStore) job(t *testing.T, id string) Job {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		t.Fatalf("job %s not found", id)
	}
	return job
}

func (s *memoryStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func (c *fakeClock) Add(duration time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
	return c.now
}

func testRegistration(sessionID string) Registration {
	return Registration{
		SessionID: sessionID,
		Name:      "check deployment",
		Prompt:    "inspect the deployment and report its health",
		Schedule:  "* * * * *",
		Timezone:  "UTC",
	}
}

func testScheduler(t *testing.T, store Store, executor Executor, clock *fakeClock, change ...func(*Options)) *Scheduler {
	t.Helper()
	options := Options{Now: clock.Now, LeaseDuration: time.Hour, FailureBackoff: time.Minute, MaxFailureBackoff: 2 * time.Minute}
	for _, update := range change {
		update(&options)
	}
	scheduler, err := NewScheduler(store, executor, options)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func registerTestJob(t *testing.T, scheduler *Scheduler, id, sessionID string) Job {
	t.Helper()
	registration := testRegistration(sessionID)
	registration.ID = id
	job, err := scheduler.Register(context.Background(), registration)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
