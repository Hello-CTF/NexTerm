package production

import (
	"context"
	"encoding/json"
	"runtime"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hostports"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

// newProductionHostPortsService runs port discovery and connectivity probes on the session's exec channel,
// after the caller has been authorized for the session.
func newProductionHostPortsService(sessions *session.Manager) *hostports.Service {
	return newProductionHostPortsServiceWithAudit(sessions, nil)
}

// hostPortsAuditor is the part of the store that records connectivity tests.
type hostPortsAuditor interface {
	AuditInsert(context.Context, store.AuditInput) error
}

// newProductionHostPortsServiceWithAudit is newProductionHostPortsService that also writes an audit record
// for every connectivity test executed on a host.
func newProductionHostPortsServiceWithAudit(sessions *session.Manager, auditor hostPortsAuditor) *hostports.Service {
	service := hostports.NewService(func(ctx context.Context, sessionID string) (hostports.Target, error) {
		ctx = productionSessionRequestContext(ctx)
		connected, err := sessions.SessionFor(ctx, sessionID)
		if err != nil {
			return hostports.Target{}, session.IPCError(err)
		}
		transport, err := sessions.TransportFor(ctx, sessionID)
		if err != nil {
			return hostports.Target{}, session.IPCError(err)
		}
		shell := hostPortsShellForGOOS(connected.Asset(), runtime.GOOS)
		// Device agents know their real platform; it overrides the asset-level guess.
		if deviceTransport, ok := transport.(*device.Transport); ok {
			shell = hostports.ShellPOSIX
			if deviceTransport.Platform() == "windows" {
				shell = hostports.ShellPowerShell
			}
		}
		return hostports.Target{
			Runner:  transport,
			Shell:   shell,
			AssetID: connected.Asset().ID,
			LastOutputAgo: func() (time.Duration, bool) {
				tabs, err := sessions.ListTabsFor(ctx)
				if err != nil {
					return 0, false
				}
				return terminalOutputAge(tabs, sessionID)
			},
		}, nil
	})
	if auditor != nil {
		service.SetAudit(hostPortsAudit(auditor))
	}
	return service
}

type hostPortsAuditPayload struct {
	Method     string `json:"method"`
	Host       string `json:"host,omitempty"`
	Port       uint16 `json:"port,omitempty"`
	URL        string `json:"url,omitempty"`
	Command    string `json:"command"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"durationMs"`
	Output     string `json:"output,omitempty"`
}

// maxHostPortsAuditOutput bounds the probe output kept in the audit trail.
const maxHostPortsAuditOutput = 512

// hostPortsAudit records each executed connectivity test with the executor (the owner carried by the
// context), session, asset, target, exact command, outcome and duration. A failure to write is dropped
// because the test has already run and its result must still reach the caller.
func hostPortsAudit(auditor hostPortsAuditor) hostports.AuditFunc {
	return func(ctx context.Context, event hostports.AuditEvent) {
		output := event.Result.Output
		if len(output) > maxHostPortsAuditOutput {
			output = output[:maxHostPortsAuditOutput]
		}
		input := store.AuditInput{
			SessionID: optionalString(event.Args.SessionID), AssetID: optionalString(event.AssetID),
			Source: "user", Kind: "port_probe",
			Payload: hostPortsAuditPayload{
				Method: event.Args.Method, Host: event.Args.Host, Port: event.Args.Port, URL: event.Args.URL,
				Command: event.Result.Command, OK: event.Result.OK, DurationMs: event.Result.DurationMs, Output: output,
			},
			DurationMS: &event.Result.DurationMs,
		}
		if event.Result.ExitCode != nil {
			code := int32(*event.Result.ExitCode)
			input.ExitCode = &code
		}
		auditCtx, cancel := context.WithTimeout(ipc.WithUserID(context.WithoutCancel(ctx), ipc.OwnerID(ctx)), 5*time.Second)
		defer cancel()
		_ = auditor.AuditInsert(auditCtx, input)
	}
}

// forgetHostPortsOnStatus drops the cached host platform when a session is no longer live.
func forgetHostPortsOnStatus(forget func(sessionID string), status session.StatusEvent) {
	if forget == nil || status.Status == session.StatusConnected || status.Status == session.StatusConnecting {
		return
	}
	forget(status.SessionID)
}

// hostPortsShellForGOOS picks the command language of the exec channel. SSH hosts stay unknown unless the
// asset names PowerShell, because Windows OpenSSH hosts default to cmd and are told apart by probing.
func hostPortsShellForGOOS(asset session.Asset, goos string) hostports.Shell {
	switch asset.Kind {
	case session.KindWinRM:
		return hostports.ShellPowerShell
	case session.KindLocal:
		if goos == "windows" {
			return hostports.ShellPowerShell
		}
		return hostports.ShellPOSIX
	case session.KindDevice:
		return hostports.ShellPOSIX
	}
	var options struct {
		Shell string `json:"shell"`
	}
	if raw, err := json.Marshal(asset.Options); err == nil {
		_ = json.Unmarshal(raw, &options)
	}
	if windowsShell(options.Shell) {
		return hostports.ShellPowerShell
	}
	return hostports.ShellUnknown
}

// terminalOutputAge is the shortest time since output over the session's terminals that have produced any.
func terminalOutputAge(tabs []session.TabInfo, sessionID string) (time.Duration, bool) {
	var best time.Duration
	found := false
	for _, tab := range tabs {
		if tab.SessionID != sessionID || tab.LastOutputAt == 0 {
			continue
		}
		age := time.Duration(max(tab.LastOutputMSAgo, 0)) * time.Millisecond
		if !found || age < best {
			best, found = age, true
		}
	}
	return best, found
}

func hostPortsModule(service *hostports.Service) Module {
	return Module{Name: "host-ports", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
		return service.RegisterCommands(dispatcher)
	}}
}
