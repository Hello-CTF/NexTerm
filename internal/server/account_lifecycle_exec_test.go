package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type accountExecLifecycleTransport struct {
	id      string
	started chan<- string
	release <-chan struct{}
	calls   *atomic.Int32
	alive   atomic.Bool
}

func newAccountExecLifecycleTransport(id string, started chan<- string, release <-chan struct{}, calls *atomic.Int32) *accountExecLifecycleTransport {
	transport := &accountExecLifecycleTransport{id: id, started: started, release: release, calls: calls}
	transport.alive.Store(true)
	return transport
}

func (t *accountExecLifecycleTransport) Kind() string       { return "ssh" }
func (t *accountExecLifecycleTransport) Generation() uint64 { return 1 }
func (t *accountExecLifecycleTransport) IsAlive() bool      { return t.alive.Load() }
func (t *accountExecLifecycleTransport) Close() error {
	t.alive.Store(false)
	return nil
}
func (t *accountExecLifecycleTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *accountExecLifecycleTransport) Exec(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
	t.calls.Add(1)
	t.started <- t.id
	select {
	case <-ctx.Done():
		return base.ExecResult{}, ctx.Err()
	case <-t.release:
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	}
}

func newAccountExecLifecycleFixture(t *testing.T, batches *execbatch.Service) *accountFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := ipc.Register(dispatcher, "exec_batch_execute", func(ctx context.Context, _ *ipc.Call, input execbatch.Request) (execbatch.Batch, error) {
		return batches.Execute(ctx, ipc.OwnerID(ctx), input)
	}); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, false)
	config.Dispatcher = dispatcher
	config.Options.Auth = AuthOn
	config.Options.Listen = "127.0.0.1:0"
	accounts := account.New(database.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")))
	config.Accounts = accounts
	config.AccountCleanup = func(ctx context.Context, ownerID, cleanupID string, sessionIDs ...string) error {
		batches.CancelOwner(ctx, ownerID, cleanupID, nil, sessionIDs...)
		return nil
	}
	config.AccountCleanupPrepare = func(ctx context.Context, ownerID, cleanupID string) (func(...string), func(), error) {
		commit, rollback := batches.PrepareOwnerCleanup(ctx, ownerID, cleanupID, nil)
		return commit, rollback, nil
	}
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{
		server: server, http: httpServer, accounts: accounts, db: database.DB(), client: &http.Client{},
	}
}

func decodeAccountExecBatch(t *testing.T, call accountCall) execbatch.Batch {
	t.Helper()
	if call.status != http.StatusOK || call.body["ok"] != true {
		t.Fatalf("execute RPC status = %d body = %v", call.status, call.body)
	}
	encoded, err := json.Marshal(call.body["data"])
	if err != nil {
		t.Fatal(err)
	}
	var batch execbatch.Batch
	if err := json.Unmarshal(encoded, &batch); err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestAccountLifecycleRevocationCancelsExecBatch(t *testing.T) {
	tests := []struct {
		name string
		act  func(*testing.T, *accountFixture, *accountTestSession) accountCall
	}{
		{
			name: "logout",
			act: func(t *testing.T, fixture *accountFixture, session *accountTestSession) accountCall {
				return fixture.call(t, http.MethodPost, "/auth/logout", nil, session, session.csrf, nil)
			},
		},
		{
			name: "logout all",
			act: func(t *testing.T, fixture *accountFixture, session *accountTestSession) accountCall {
				return fixture.call(t, http.MethodPost, "/auth/logout-all", nil, session, session.csrf, nil)
			},
		},
		{
			name: "session revoke",
			act: func(t *testing.T, fixture *accountFixture, session *accountTestSession) accountCall {
				sessionID := currentAccountSessionID(t, fixture, session)
				return fixture.call(t, http.MethodDelete, "/auth/sessions/"+sessionID, nil, session, session.csrf, nil)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan string, 4)
			releaseB := make(chan struct{})
			var calls atomic.Int32
			transports := map[string]*accountExecLifecycleTransport{
				"a-first":  newAccountExecLifecycleTransport("a-first", started, make(chan struct{}), &calls),
				"a-queued": newAccountExecLifecycleTransport("a-queued", started, make(chan struct{}), &calls),
				"b-target": newAccountExecLifecycleTransport("b-target", started, releaseB, &calls),
			}
			targets := make(map[string]execbatch.Target, len(transports))
			for id, transport := range transports {
				targets[id] = execbatch.Target{SessionID: id, SessionName: id, Transport: transport, Generation: transport.Generation()}
			}
			batches := execbatch.New(execbatch.Config{Resolver: execbatch.ResolverFunc(func(_ context.Context, sessionID string) (execbatch.Target, error) {
				target, ok := targets[sessionID]
				if !ok {
					return execbatch.Target{}, execbatch.ErrDisconnected
				}
				return target, nil
			})})
			fixture := newAccountExecLifecycleFixture(t, batches)
			alice, _ := fixture.initSuperadmin(t, "alice", "password-alice")
			if _, err := fixture.accounts.CreateUser(context.Background(), "bob", "", "password-bob"); err != nil {
				t.Fatal(err)
			}
			bob := fixture.login(t, "bob", "password-bob")

			execute := func(session *accountTestSession, request execbatch.Request) chan accountCall {
				done := make(chan accountCall, 1)
				go func() {
					done <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
						"cmd": "exec_batch_execute", "args": request,
					}, session, session.csrf, nil)
				}()
				return done
			}
			doneA := execute(alice, execbatch.Request{
				BatchID: "http-account-a", SessionIDs: []string{"a-first", "a-queued"}, Command: "wait", MaxConcurrent: 1,
			})
			doneB := execute(bob, execbatch.Request{
				BatchID: "http-account-b", SessionIDs: []string{"b-target"}, Command: "wait",
			})
			seen := make(map[string]bool, 2)
			for range 2 {
				select {
				case id := <-started:
					seen[id] = true
				case <-time.After(2 * time.Second):
					t.Fatal("account batches did not start")
				}
			}
			if !seen["a-first"] || !seen["b-target"] {
				t.Fatalf("unexpected initial Exec targets: %v", seen)
			}

			if call := test.act(t, fixture, alice); call.status != http.StatusOK {
				t.Fatalf("account lifecycle status = %d body = %v", call.status, call.body)
			}
			select {
			case call := <-doneA:
				batch := decodeAccountExecBatch(t, call)
				if len(batch.Results) != 2 {
					t.Fatalf("owner A results = %+v", batch.Results)
				}
				for _, result := range batch.Results {
					if result.Status != execbatch.StatusCanceled {
						t.Fatalf("owner A result = %+v", result)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("owner A execute RPC did not stop")
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("Exec calls = %d, want A's first target and B only", got)
			}

			close(releaseB)
			select {
			case call := <-doneB:
				batch := decodeAccountExecBatch(t, call)
				if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusSuccess {
					t.Fatalf("owner B was affected by A revocation: %+v", batch)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("owner B did not finish normally")
			}
		})
	}
}
