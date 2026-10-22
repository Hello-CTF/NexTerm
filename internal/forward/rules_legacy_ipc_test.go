package forward

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func registerLegacyForwardCommands(t *testing.T, service *Service) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func dispatchLegacyForward(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, command string, args any) ipc.Response {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: encoded}, ipc.Environment{})
}

func requireLegacyForwardCode(t *testing.T, response ipc.Response, code ipc.Code) {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %+v, want error code %s", response, code)
	}
}

func TestSavedRuleLegacyRetryRechecksAssetTombstone(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
		t.Fatal(err)
	}
	fixture.service.FailSession(fixture.connected.ID, "session disconnected")
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 || runtimes[0].Status != StatusFailed {
		t.Fatalf("failed saved runtime = %+v", runtimes)
	}
	if err := fixture.db.AssetDelete(fixture.ctx, fixture.asset.ID); err != nil {
		t.Fatal(err)
	}
	var retryListeners atomic.Int32
	fixture.service.listen = func(network, address string) (net.Listener, error) {
		retryListeners.Add(1)
		return net.Listen(network, address)
	}
	dispatcher := registerLegacyForwardCommands(t, fixture.service)
	response := dispatchLegacyForward(t, dispatcher, fixture.ctx, "forward_retry", retryRequest{ID: rule.ID, SessionID: fixture.connected.ID})
	requireLegacyForwardCode(t, response, ipc.CodeBadParam)
	if got := retryListeners.Load(); got != 0 {
		t.Fatalf("retry created %d listeners after asset tombstone", got)
	}
	probe, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(rule.ListenPort))))
	if err != nil {
		t.Fatalf("saved rule port is still occupied after rejected retry: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID); err != nil {
		t.Fatalf("rejected retry removed definition: %v", err)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 || runtimes[0].Status != StatusFailed {
		t.Fatalf("rejected retry changed failed runtime: %+v", runtimes)
	}
}

func TestSavedRuleLegacyRetryRejectsChangedPersistentConfig(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
		t.Fatal(err)
	}
	fixture.service.FailSession(fixture.connected.ID, "session disconnected")
	if _, err := fixture.db.ForwardRuleSave(fixture.ctx, RuleInput{
		ID: rule.ID, AssetID: fixture.asset.ID, Name: rule.Name, ListenPort: rule.ListenPort,
		TargetHost: "changed-without-lifecycle.internal", TargetPort: rule.TargetPort,
	}); err != nil {
		t.Fatal(err)
	}
	var retryListeners atomic.Int32
	fixture.service.listen = func(network, address string) (net.Listener, error) {
		retryListeners.Add(1)
		return net.Listen(network, address)
	}
	dispatcher := registerLegacyForwardCommands(t, fixture.service)
	response := dispatchLegacyForward(t, dispatcher, fixture.ctx, "forward_retry", retryRequest{ID: rule.ID})
	requireLegacyForwardCode(t, response, ipc.CodeBadParam)
	if got := retryListeners.Load(); got != 0 {
		t.Fatalf("retry created %d listeners with inconsistent fixed config", got)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 || runtimes[0].Status != StatusFailed {
		t.Fatalf("config rejection changed failed runtime: %+v", runtimes)
	}
}

func TestSavedRuleLegacyRetryPreservesPersistentLifecycle(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	observer := &observingRuleStore{RuleStore: fixture.db}
	fixture.service.rules = observer
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
		t.Fatal(err)
	}
	fixture.service.FailSession(fixture.connected.ID, "session disconnected")
	fixture.service.listen = func(network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err != nil {
			return nil, err
		}
		return &failingCloseListener{Listener: listener, err: errors.New("close rejected after retry")}, nil
	}
	dispatcher := registerLegacyForwardCommands(t, fixture.service)
	response := dispatchLegacyForward(t, dispatcher, fixture.ctx, "forward_retry", retryRequest{ID: rule.ID})
	if !response.OK {
		t.Fatalf("legacy retry with remembered session = %+v", response.Error)
	}
	var spec Spec
	if err := json.Unmarshal(response.Data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.ID != rule.ID || spec.Status != StatusActive || spec.ListenHost != savedListenHost || spec.ListenPort != uint16(rule.ListenPort) {
		t.Fatalf("retried saved runtime = %+v", spec)
	}
	response = dispatchLegacyForward(t, dispatcher, fixture.ctx, "forward_remove", removeRequest{ID: rule.ID})
	requireLegacyForwardCode(t, response, ipc.CodeIO)
	err := fixture.service.DeleteRule(fixture.ctx, rule.ID)
	requireSavedRuleCode(t, err, ipc.CodeIO)
	if observer.deletes != 0 {
		t.Fatalf("definition deleted %d times after retried close failure", observer.deletes)
	}
	if _, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID); err != nil {
		t.Fatalf("retried definition lost after close failure: %v", err)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 || runtimes[0].ID != rule.ID {
		t.Fatalf("retried runtime mapping lost after close failure: %+v", runtimes)
	}
}

func TestSavedRuleLegacyRemoveFailureStillBlocksDeleteRule(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	observer := &observingRuleStore{RuleStore: fixture.db}
	fixture.service.rules = observer
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	fixture.service.listen = func(network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err != nil {
			return nil, err
		}
		return &failingCloseListener{Listener: listener, err: errors.New("close rejected")}, nil
	}
	if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
		t.Fatal(err)
	}
	dispatcher := registerLegacyForwardCommands(t, fixture.service)
	response := dispatchLegacyForward(t, dispatcher, fixture.ctx, "forward_remove", removeRequest{ID: rule.ID})
	requireLegacyForwardCode(t, response, ipc.CodeIO)
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 || runtimes[0].ID != rule.ID {
		t.Fatalf("legacy remove dropped runtime after close failure: %+v", runtimes)
	}
	err := fixture.service.DeleteRule(fixture.ctx, rule.ID)
	requireSavedRuleCode(t, err, ipc.CodeIO)
	if observer.deletes != 0 {
		t.Fatalf("definition deleted %d times after close failure", observer.deletes)
	}
	if _, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID); err != nil {
		t.Fatalf("definition lost after legacy remove failure: %v", err)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 || runtimes[0].ID != rule.ID {
		t.Fatalf("DeleteRule dropped runtime after close failure: %+v", runtimes)
	}
}

type blockingCloseListener struct {
	net.Listener
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *blockingCloseListener) Close() error {
	l.once.Do(func() {
		close(l.entered)
		<-l.release
	})
	return l.Listener.Close()
}

func installBlockingCloseListener(t *testing.T, fixture *savedRuleFixture) (*blockingCloseListener, func()) {
	t.Helper()
	listener := &blockingCloseListener{entered: make(chan struct{}), release: make(chan struct{})}
	fixture.service.listen = func(network, address string) (net.Listener, error) {
		inner, err := net.Listen(network, address)
		if err != nil {
			return nil, err
		}
		listener.Listener = inner
		return listener, nil
	}
	var once sync.Once
	release := func() { once.Do(func() { close(listener.release) }) }
	t.Cleanup(release)
	return listener, release
}

func waitForLegacyClose(t *testing.T, listener *blockingCloseListener) {
	t.Helper()
	select {
	case <-listener.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy remove did not reach listener close")
	}
}

func TestSavedRuleLegacyRemoveSerializesConcurrentEditAndDelete(t *testing.T) {
	t.Run("edit", func(t *testing.T) {
		fixture := newSavedRuleFixture(t)
		rule := fixture.saveRule(t, freeSavedRulePort(t))
		listener, release := installBlockingCloseListener(t, fixture)
		if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
			t.Fatal(err)
		}
		dispatcher := registerLegacyForwardCommands(t, fixture.service)
		removeResult := make(chan ipc.Response, 1)
		go func() {
			removeResult <- dispatchLegacyForward(t, dispatcher, fixture.ctx, "forward_remove", removeRequest{ID: rule.ID})
		}()
		waitForLegacyClose(t, listener)

		edit := RuleInput{
			ID: rule.ID, AssetID: fixture.asset.ID, Name: "edited-after-stop", ListenPort: rule.ListenPort,
			TargetHost: "changed.internal", TargetPort: 4567,
		}
		editStarted := make(chan struct{})
		editResult := make(chan error, 1)
		go func() {
			close(editStarted)
			_, err := fixture.service.SaveRule(fixture.ctx, edit)
			editResult <- err
		}()
		<-editStarted
		select {
		case err := <-editResult:
			t.Fatalf("edit completed before legacy remove finished closing: %v", err)
		case <-time.After(75 * time.Millisecond):
		}
		if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 {
			t.Fatalf("runtime mapping removed before close completed: %+v", runtimes)
		}
		release()
		if response := <-removeResult; !response.OK {
			t.Fatalf("legacy remove = %+v", response.Error)
		}
		if err := <-editResult; err != nil {
			t.Fatalf("serialized edit after stop: %v", err)
		}
		stored, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID)
		if err != nil || stored.TargetHost != "changed.internal" {
			t.Fatalf("edited definition = %+v, %v", stored, err)
		}
		if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
			t.Fatalf("runtime survived serialized edit: %+v", runtimes)
		}
	})

	t.Run("delete", func(t *testing.T) {
		fixture := newSavedRuleFixture(t)
		rule := fixture.saveRule(t, freeSavedRulePort(t))
		listener, release := installBlockingCloseListener(t, fixture)
		if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
			t.Fatal(err)
		}
		dispatcher := registerLegacyForwardCommands(t, fixture.service)
		removeResult := make(chan ipc.Response, 1)
		go func() {
			removeResult <- dispatchLegacyForward(t, dispatcher, fixture.ctx, "forward_remove", removeRequest{ID: rule.ID})
		}()
		waitForLegacyClose(t, listener)

		deleteStarted := make(chan struct{})
		deleteResult := make(chan error, 1)
		go func() {
			close(deleteStarted)
			deleteResult <- fixture.service.DeleteRule(fixture.ctx, rule.ID)
		}()
		<-deleteStarted
		select {
		case err := <-deleteResult:
			t.Fatalf("delete completed before legacy remove finished closing: %v", err)
		case <-time.After(75 * time.Millisecond):
		}
		if _, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID); err != nil {
			t.Fatalf("definition deleted before close completed: %v", err)
		}
		if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 {
			t.Fatalf("runtime mapping removed before close completed: %+v", runtimes)
		}
		release()
		if response := <-removeResult; !response.OK {
			t.Fatalf("legacy remove = %+v", response.Error)
		}
		if err := <-deleteResult; err != nil {
			t.Fatalf("serialized DeleteRule: %v", err)
		}
		_, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID)
		requireSavedRuleCode(t, err, ipc.CodeNotFound)
		if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
			t.Fatalf("runtime survived serialized delete: %+v", runtimes)
		}
	})
}
