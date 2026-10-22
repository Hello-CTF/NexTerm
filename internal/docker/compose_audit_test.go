package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type composeAuditStream struct {
	*bufferCommandStream
	code int
}

func (s *composeAuditStream) Wait(context.Context) (int, error) {
	return s.code, nil
}

func composeAuditCLI() *fakeDockerCLI {
	return &fakeDockerCLI{handler: func(command string) CommandResult {
		switch command {
		case "docker 'ps' '-a' '--format' '{{json .}}'":
			return CommandResult{Stdout: composePS("c1", "web", "shop", "web")}
		case "docker 'inspect' 'c1'":
			return CommandResult{Stdout: composeInspect("shop", "/srv/shop", "compose.yml")}
		default:
			return CommandResult{}
		}
	}}
}

func waitComposeAudit(t *testing.T, events <-chan AuditEvent) AuditEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for redeploy audit")
		return AuditEvent{}
	}
}

func TestComposeRedeployAuditStartupFailure(t *testing.T) {
	events := make(chan AuditEvent, 1)
	opener := &recordingOpener{err: errors.New("open stream failed")}
	service := NewService(
		&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(composeAuditCLI(), opener, ShellPOSIX)},
		WithAuditor(AuditorFunc(func(_ context.Context, event AuditEvent) error {
			events <- event
			return nil
		})),
	)
	_, err := service.AttachCompose(t.Context(), ComposeAttachRequest{
		SessionID: "s1", AssetID: "asset-1", Source: "user", Project: "shop", Action: ComposeActionUp,
		ContentHash: "content-hash", Sink: newCollectSink(),
	})
	if err == nil {
		t.Fatal("expected stream startup failure")
	}
	event := waitComposeAudit(t, events)
	if event.Kind != "docker_compose_redeploy" || event.ExitCode != -1 || event.SessionID != "s1" || event.AssetID != "asset-1" {
		t.Fatalf("startup audit = %+v", event)
	}
	if event.Payload["project"] != "shop" || event.Payload["action"] != "up" || event.Payload["contentHash"] != "content-hash" {
		t.Fatalf("startup audit payload = %+v", event.Payload)
	}
}

func TestComposeRedeployAuditProcessExit(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
	}{
		{name: "success", code: 0},
		{name: "nonzero", code: 17},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := make(chan AuditEvent, 1)
			stream := &composeAuditStream{bufferCommandStream: &bufferCommandStream{Reader: strings.NewReader("up output")}, code: test.code}
			opener := &recordingOpener{stream: stream}
			service := NewService(
				&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(composeAuditCLI(), opener, ShellPOSIX)},
				WithAuditor(AuditorFunc(func(_ context.Context, event AuditEvent) error {
					events <- event
					return nil
				})),
			)
			id, err := service.AttachCompose(t.Context(), ComposeAttachRequest{
				SessionID: "s1", AssetID: "asset-1", Source: "user", Project: "shop", Action: ComposeActionUp,
				ContentHash: "content-hash", Sink: newCollectSink(),
			})
			if err != nil {
				t.Fatal(err)
			}
			done, err := service.StreamDone(id)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for compose stream")
			}
			event := waitComposeAudit(t, events)
			if event.Kind != "docker_compose_redeploy" || event.ExitCode != test.code {
				t.Fatalf("process audit = %+v, want exit %d", event, test.code)
			}
			if event.Payload["contentHash"] != "content-hash" || event.Payload["project"] != "shop" {
				t.Fatalf("process audit payload = %+v", event.Payload)
			}
		})
	}
}

func TestInvalidatePrunePreviewAfterBackendFailure(t *testing.T) {
	cli := &fakeDockerCLI{handler: func(command string) CommandResult {
		switch command {
		case "docker 'system' 'df'", "docker 'system' 'df' '-v'":
			return CommandResult{Stdout: testDFOutput()}
		case "docker 'ps' '-a' '--format' '{{json .}}'":
			return CommandResult{Stdout: "{}\n"}
		case "docker 'volume' 'ls' '--format' '{{json .}}'", "docker 'network' 'ls' '--format' '{{json .}}'":
			return CommandResult{}
		default:
			return CommandResult{}
		}
	}}
	provider := &stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(cli, nil, ShellPOSIX)}
	service := NewService(provider)
	preview, err := service.PreviewPrune(t.Context(), "s1", []PruneKind{PruneKindContainers})
	if err != nil {
		t.Fatal(err)
	}
	provider.commandErr = errors.New("backend unavailable")
	if _, err := service.Prune(t.Context(), PruneRequest{SessionID: "s1", Token: preview.Token, Digest: preview.Digest}); err == nil {
		t.Fatal("expected backend failure")
	}
	service.InvalidatePrunePreview("s1", preview.Token)
	provider.commandErr = nil
	if _, err := service.Prune(t.Context(), PruneRequest{SessionID: "s1", Token: preview.Token, Digest: preview.Digest}); err == nil {
		t.Fatal("invalidated preview token was accepted")
	}
	if got := cli.count("docker 'container' 'rm'"); got != 0 {
		t.Fatalf("remove commands after backend failure = %d", got)
	}
}
