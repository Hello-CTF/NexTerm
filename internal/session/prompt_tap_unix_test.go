//go:build unix

package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// A prompt detected by the helper reaches every device with its tap
// identity; the first device's tap writes the option's keys, and the other
// device's tap of the same prompt instance is stale and writes nothing.
func TestPromptTapAnswersOnceAcrossDevices(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-tap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = instance.Close()
	})
	terminals := newFakeTerminalFactory()
	newDevice := func(terminals *fakeTerminalFactory) (*Manager, *attentionRecorder) {
		provider := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
		recorder := newAttentionRecorder()
		manager := NewManager(Config{
			Connector: newFakeConnector(), Terminals: terminals, Emitter: recorder, OpenAccess: true,
			DurableResolver: DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
				return provider, nil
			}),
		})
		t.Cleanup(func() { _ = manager.Close() })
		return manager, recorder
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	desktop, desktopEvents := newDevice(terminals)
	phone, phoneEvents := newDevice(newFakeTerminalFactory())

	tabID := ids.New()
	desktopSession, err := desktop.Connect(ctx, Asset{ID: "ssh-tap-a", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: desktopSession.ID, ClientID: "client-a", ChannelID: "tap-a1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", `printf 'Apply? [y/N] '; read answer; printf '\ngot:%s\n' "$answer"; sleep 30`}},
	}); err != nil {
		t.Fatal(err)
	}
	phoneSession, err := phone.Connect(ctx, Asset{ID: "ssh-tap-b", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := phone.ListRemoteDurable(ctx, phoneSession.ID)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("remote descriptors = %+v, %v", descriptors, err)
	}
	if _, err := phone.RecoverRemoteDurable(ctx, descriptors[0].ID, OpenTabOptions{ClientID: "client-b", ChannelID: "tap-b1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}

	tappable := func(event AttentionEvent) bool { return event.Prompt != nil && event.Prompt.Tappable }
	asked := desktopEvents.wait(t, tabID, "tappable prompt on the desktop", tappable)
	phoneAsked := phoneEvents.wait(t, tabID, "tappable prompt on the phone", tappable)
	prompt := phoneAsked.Prompt
	if prompt.Kind != "yesno" || prompt.Question == "" || prompt.Generation == "" || prompt.Submit != base.TapSubmitEnter ||
		len(prompt.Options) != 2 || prompt.Epoch != asked.Prompt.Epoch || prompt.Fingerprint != asked.Prompt.Fingerprint {
		t.Fatalf("phone prompt %+v, desktop prompt %+v", prompt, asked.Prompt)
	}
	request := PromptTapRequest{Generation: prompt.Generation, Epoch: prompt.Epoch, Fingerprint: prompt.Fingerprint, Option: 0}
	if err := phone.TapPromptFor(ctx, phoneAsked.TabID, "client-b", request); err != nil {
		t.Fatal(err)
	}
	if err := desktop.TapPromptFor(ctx, tabID, "client-a", request); !errors.Is(err, base.ErrStalePrompt) {
		t.Fatalf("second tap of the same prompt = %v, want ErrStalePrompt", err)
	}
	stale := request
	stale.Generation = "another-helper"
	if err := phone.TapPromptFor(ctx, phoneAsked.TabID, "client-b", stale); !errors.Is(err, base.ErrStalePrompt) {
		t.Fatalf("tap from another helper generation = %v, want ErrStalePrompt", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for !bytes.Contains(terminals.terminal(tabID).bytes(), []byte("got:y\r\n")) {
		if time.Now().After(deadline) {
			t.Fatalf("desktop output = %q, want exactly one answer", terminals.terminal(tabID).bytes())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The tap counts as the phone client's input.
	tab, err := phone.Tab(phoneAsked.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if last := tab.LastInputClient(); last != "client-b" {
		t.Fatalf("last input client after the tap = %q", last)
	}
}
