package production

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
)

type execBatchSSHFixture struct {
	listener net.Listener
	config   *gossh.ServerConfig
	requests chan string

	execCount atomic.Int32

	blockStart     chan struct{}
	unblockStart   sync.Once
	reportExit     chan struct{}
	reportExitOnce sync.Once
}

func newExecBatchSSHFixture(t *testing.T) *execBatchSSHFixture {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			if metadata.User() == "test" && string(password) == "secret" {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid test credentials")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &execBatchSSHFixture{
		listener: listener, config: config, requests: make(chan string, 16),
		blockStart: make(chan struct{}), reportExit: make(chan struct{}),
	}
	go fixture.accept()
	t.Cleanup(func() {
		_ = fixture.listener.Close()
		fixture.unblockStart.Do(func() { close(fixture.blockStart) })
		fixture.reportExitOnce.Do(func() { close(fixture.reportExit) })
	})
	return fixture
}

func (f *execBatchSSHFixture) port() int {
	return f.listener.Addr().(*net.TCPAddr).Port
}

func (f *execBatchSSHFixture) allowStart() {
	f.unblockStart.Do(func() { close(f.blockStart) })
}

func (f *execBatchSSHFixture) signalExit() {
	f.reportExitOnce.Do(func() { close(f.reportExit) })
}

func (f *execBatchSSHFixture) accept() {
	for {
		connection, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handleConnection(connection)
	}
}

func (f *execBatchSSHFixture) handleConnection(connection net.Conn) {
	serverConnection, channels, requests, err := gossh.NewServerConn(connection, f.config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer serverConnection.Close()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(gossh.UnknownChannelType, "exec batch fixture")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err == nil {
			go f.handleSession(channel, channelRequests)
		}
	}
}

func (f *execBatchSSHFixture) handleSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			continue
		}
		f.execCount.Add(1)
		f.requests <- payload.Command
		switch payload.Command {
		case "block-start":
			<-f.blockStart
			_ = request.Reply(true, nil)
			sendExecBatchExitStatus(channel, 0)
		case "race-exit":
			_ = request.Reply(true, nil)
			<-f.reportExit
			sendExecBatchExitStatus(channel, 23)
		case "hold-exit":
			_ = request.Reply(true, nil)
			<-f.reportExit
			sendExecBatchExitStatus(channel, 0)
		case "normal-exit":
			_ = request.Reply(true, nil)
			sendExecBatchExitStatus(channel, 23)
		default:
			_ = request.Reply(true, nil)
			sendExecBatchExitStatus(channel, 0)
		}
		return
	}
}

func sendExecBatchExitStatus(channel gossh.Channel, status uint32) {
	_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{status}))
}

func newExecBatchSSHProduction(t *testing.T, fixture *execBatchSSHFixture, targetCount int) (*Production, []sessionInfoDTO) {
	t.Helper()
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credentialID := createConnectorTestCredential(t, production, "batch-race-password", "secret")
	connected := make([]sessionInfoDTO, 0, targetCount)
	for index := range targetCount {
		name := fmt.Sprintf("batch-race-target-%d", index)
		assetID := createConnectorTestAsset(t, production, name, "127.0.0.1", fixture.port(), "password", credentialID, `{"autoAcceptUnknownHost":true}`)
		connected = append(connected, connectHostKeyTestAsset(t, production, assetID))
	}
	return production, connected
}

func TestExecBatchProductionSSHStartCancellationBoundaries(t *testing.T) {
	fixture := newExecBatchSSHFixture(t)
	production, connected := newExecBatchSSHProduction(t, fixture, 2)

	cancelResponse := dispatchHostKeyTest(t, production, "exec_batch_cancel", `{"batchId":"ssh-early-cancel"}`)
	decodeExecBatchTest[execbatch.CancelResult](t, cancelResponse)
	earlyResponse := dispatchHostKeyTest(t, production, "exec_batch_execute", `{"batchId":"ssh-early-cancel","sessionIds":["`+connected[0].ID+`"],"command":"early-no-start"}`)
	earlyBatch := decodeExecBatchTest[execbatch.Batch](t, earlyResponse)
	if earlyBatch.Results[0].Status != execbatch.StatusCanceled || fixture.execCount.Load() != 0 {
		t.Fatalf("cancel before Start reached SSH: batch=%+v requests=%d", earlyBatch, fixture.execCount.Load())
	}
	assertExecBatchSSHAudit(t, production, 0, "")

	executeDone := make(chan ipc.Response, 1)
	go func() {
		executeDone <- dispatchHostKeyTest(t, production, "exec_batch_execute", `{"batchId":"ssh-start-race","sessionIds":["`+connected[0].ID+`","`+connected[1].ID+`"],"command":"block-start","maxConcurrent":1,"timeoutMs":5000}`)
	}()
	select {
	case command := <-fixture.requests:
		if command != "block-start" {
			t.Fatalf("first SSH command = %q", command)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSH Start did not reach the blocking fixture")
	}
	cancelDone := make(chan ipc.Response, 1)
	go func() {
		cancelDone <- dispatchHostKeyTest(t, production, "exec_batch_cancel", `{"batchId":"ssh-start-race"}`)
	}()
	select {
	case response := <-cancelDone:
		decodeExecBatchTest[execbatch.CancelResult](t, response)
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not interrupt the in-flight SSH Start")
	}
	fixture.allowStart()
	var batch execbatch.Batch
	select {
	case response := <-executeDone:
		batch = decodeExecBatchTest[execbatch.Batch](t, response)
	case <-time.After(3 * time.Second):
		t.Fatal("execute RPC did not finish after Start cancellation")
	}
	if fixture.execCount.Load() != 1 {
		t.Fatalf("queued command reached SSH after cancel: %d requests", fixture.execCount.Load())
	}
	for _, result := range batch.Results {
		if result.Status != execbatch.StatusCanceled {
			t.Fatalf("Start cancellation result = %+v", result)
		}
	}
	assertExecBatchSSHAudit(t, production, 1, execbatch.StatusCanceled)
}

type execBatchSignalTransport struct {
	base.Transport
	signal func()
}

func (t *execBatchSignalTransport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	stop := context.AfterFunc(ctx, t.signal)
	result, err := t.Transport.Exec(ctx, command, options)
	stop()
	return result, err
}

func TestExecBatchSSHOperationContextRaces(t *testing.T) {
	for _, test := range []struct {
		name      string
		command   string
		timeoutMS int64
		cancel    bool
		signal    bool
		want      execbatch.Status
	}{
		{name: "cancel wins over remote exit", command: "race-exit", cancel: true, signal: true, want: execbatch.StatusCanceled},
		{name: "timeout wins over remote exit", command: "race-exit", timeoutMS: 20, signal: true, want: execbatch.StatusTimeout},
		{name: "ordinary non-zero exit", command: "normal-exit", want: execbatch.StatusExitError},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecBatchSSHFixture(t)
			production, connected := newExecBatchSSHProduction(t, fixture, 1)
			ctx := session.WithOpenAccess(t.Context())
			transport, err := production.Services.Sessions.TransportFor(ctx, connected[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			var execTransport base.Transport = transport
			if test.signal {
				execTransport = &execBatchSignalTransport{Transport: transport, signal: fixture.signalExit}
			}
			assetID := ""
			if connected[0].AssetID != nil {
				assetID = *connected[0].AssetID
			}
			service := execbatch.New(execbatch.Config{
				Resolver: execbatch.ResolverFunc(func(_ context.Context, sessionID string) (execbatch.Target, error) {
					return execbatch.Target{
						SessionID: sessionID, SessionName: connected[0].Name, AssetID: assetID,
						Transport: execTransport, Generation: transport.Generation(),
					}, nil
				}),
				Auditor: production.Services.Store,
			})
			request := execbatch.Request{
				BatchID: "ssh-" + test.name, SessionIDs: []string{connected[0].ID}, Command: test.command, TimeoutMS: test.timeoutMS,
			}
			var batch execbatch.Batch
			if test.cancel {
				type outcome struct {
					batch execbatch.Batch
					err   error
				}
				done := make(chan outcome, 1)
				go func() {
					batch, err := service.Execute(t.Context(), "owner-a", request)
					done <- outcome{batch: batch, err: err}
				}()
				select {
				case <-fixture.requests:
				case <-time.After(3 * time.Second):
					t.Fatal("race command did not reach SSH")
				}
				if _, err := service.Cancel(t.Context(), "owner-a", request.BatchID); err != nil {
					t.Fatal(err)
				}
				select {
				case result := <-done:
					if result.err != nil {
						t.Fatal(result.err)
					}
					batch = result.batch
				case <-time.After(3 * time.Second):
					t.Fatal("cancel race execution did not finish")
				}
			} else {
				batch, err = service.Execute(t.Context(), "owner-a", request)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(batch.Results) != 1 || batch.Results[0].Status != test.want {
				t.Fatalf("SSH race result = %+v, want %s", batch.Results, test.want)
			}
			assertExecBatchSSHAudit(t, production, 1, test.want)
		})
	}
}

func assertExecBatchSSHAudit(t *testing.T, production *Production, count int, want execbatch.Status) {
	t.Helper()
	source, kind := "user", "exec_batch"
	records, err := production.Services.Store.AuditQuery(t.Context(), store.AuditQuery{Source: &source, Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != count {
		t.Fatalf("audit records = %d, want %d: %+v", len(records), count, records)
	}
	if count == 0 {
		return
	}
	var payload struct {
		Status execbatch.Status `json:"status"`
	}
	if err := json.Unmarshal([]byte(records[0].PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != want {
		t.Fatalf("audit status = %s, want %s: %s", payload.Status, want, records[0].PayloadJSON)
	}
}
