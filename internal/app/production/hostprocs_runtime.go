package production

import (
	"context"
	"runtime"

	"github.com/Hello-CTF/NexTerm/internal/hostports"
	"github.com/Hello-CTF/NexTerm/internal/hostprocs"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

// Commands run only on the session's remote exec channel; kind=local sessions are rejected by the server
// group when the session is created in multi-user mode, so the server never reaches the local machine here.

// newProductionHostProcsService lists and ends processes on the session's exec channel, after the caller
// has been authorized for the session. With a nil database kills are not audited.
func newProductionHostProcsService(sessions *session.Manager, database *store.Store) *hostprocs.Service {
	var auditor hostprocs.Auditor
	if database != nil {
		auditor = hostprocs.AuditorFunc(func(ctx context.Context, event hostprocs.AuditEvent) error {
			return database.AuditInsert(ctx, hostProcsAuditInput(event))
		})
	}
	return hostprocs.NewService(func(ctx context.Context, sessionID string) (hostprocs.Target, error) {
		ctx = productionSessionRequestContext(ctx)
		connected, err := sessions.SessionFor(ctx, sessionID)
		if err != nil {
			return hostprocs.Target{}, session.IPCError(err)
		}
		transport, err := sessions.TransportFor(ctx, sessionID)
		if err != nil {
			return hostprocs.Target{}, session.IPCError(err)
		}
		asset := connected.Asset()
		shell := hostPortsShellForGOOS(asset, runtime.GOOS)
		// Device agents know their real platform; it overrides the asset-level guess.
		if deviceTransport, ok := transport.(*device.Transport); ok {
			shell = hostports.ShellPOSIX
			if deviceTransport.Platform() == "windows" {
				shell = hostports.ShellPowerShell
			}
		}
		return hostprocs.Target{Runner: transport, Shell: hostProcsShell(shell), AssetID: asset.ID, Host: asset.Name}, nil
	}, auditor)
}

func hostProcsShell(shell hostports.Shell) hostprocs.Shell {
	switch shell {
	case hostports.ShellPOSIX:
		return hostprocs.ShellPOSIX
	case hostports.ShellPowerShell:
		return hostprocs.ShellPowerShell
	}
	return hostprocs.ShellUnknown
}

// hostProcsAuditInput maps a kill attempt to its audit row; the owner comes from the context at insert time.
func hostProcsAuditInput(event hostprocs.AuditEvent) store.AuditInput {
	input := store.AuditInput{
		Source: "hosts",
		Kind:   "process_kill",
		Payload: map[string]any{
			"host": event.Host, "pid": event.PID, "name": event.Name, "signal": event.Signal,
			"command": event.Command, "result": event.Result, "output": event.Output,
		},
	}
	if event.SessionID != "" {
		input.SessionID = &event.SessionID
	}
	if event.AssetID != "" {
		input.AssetID = &event.AssetID
	}
	if event.ExitCode != nil {
		code := int32(*event.ExitCode)
		input.ExitCode = &code
	}
	duration := event.Duration.Milliseconds()
	input.DurationMS = &duration
	return input
}

func hostProcsModule(service *hostprocs.Service) Module {
	return Module{Name: "host-processes", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
		return service.RegisterCommands(dispatcher)
	}}
}
