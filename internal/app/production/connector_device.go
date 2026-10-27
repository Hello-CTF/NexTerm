package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

// DeviceBridgeDialFunc opens one bridge of the given purpose to a device, keyed by device ID:
// "terminal" reaches the supervisor helper, "file"/"forward"/"listen"/"accept" reach the agent's purpose servers.
type DeviceBridgeDialFunc func(ctx context.Context, deviceID, purpose string) (net.Conn, error)

// DeviceConnectorConfig is the dialing surface for device sessions, injected by the wiring layer per host: Dial opens a
// bridge to the device supervisor helper (desktop = WS bridge via the logged-in server account, server = fleet registry
// direct dial); Digest resolves the device supervisor status summary (server reads the control channel hello, desktop
// queries the device list); Authorize is injected server-side only and performs owner/revocation/terminal-switch checks
// with a terminal_open audit on every (re)connect, nil on desktop (every bridge is already authorized and audited per
// attempt at the server HTTP edge). Reconnects run in a background ctx where userID is unavailable, so the IPC layer
// writes the authenticated user into asset options as the fallback.
type DeviceConnectorConfig struct {
	Dial      DeviceBridgeDialFunc
	Digest    func(ctx context.Context, deviceID string) (string, error)
	Authorize func(ctx context.Context, userID, deviceID string) error
	// Platform resolves the agent's GOOS for the exec wrapper (windows selects PowerShell); nil means unknown
	// and falls back to the POSIX default.
	Platform func(ctx context.Context, deviceID string) (string, error)
}

// SetDeviceConnector injects the device session dialing surface; without it, device session connections report unsupported.
func (s *ProductionServices) SetDeviceConnector(config *DeviceConnectorConfig) {
	if s.sshConnector != nil {
		s.sshConnector.device.Store(config)
	}
}

func (c *productionConnector) connectDevice(ctx context.Context, options productionAssetOptions, generation uint64) (base.Transport, error) {
	config := c.device.Load()
	if config == nil || config.Dial == nil || config.Digest == nil {
		return nil, fmt.Errorf("%w: 设备终端通道未配置", session.ErrUnsupported)
	}
	if options.DeviceID == "" {
		return nil, ipc.BadParam(errors.New("设备会话缺少 deviceId"))
	}
	if config.Authorize != nil {
		userID, _ := ipc.UserIDFromContext(ctx)
		if userID == "" {
			userID = options.UserID
		}
		if err := config.Authorize(ctx, userID, options.DeviceID); err != nil {
			return nil, err
		}
	}
	digest, err := config.Digest(ctx, options.DeviceID)
	if err != nil {
		return nil, err
	}
	dial := func(ctx context.Context, purpose string) (net.Conn, error) {
		return config.Dial(ctx, options.DeviceID, purpose)
	}
	platform := ""
	if config.Platform != nil {
		// Best-effort: an unknown platform falls back to the POSIX exec wrapper rather than blocking the session.
		platform, _ = config.Platform(ctx, options.DeviceID)
	}
	return device.New(options.DeviceID, digest, dial, generation, platform), nil
}

// productionDurableResolver routes durable providers by transport: device transports connect directly to the device
// supervisor helper, SSH transports go through the target machine's supervisor daemon.
type productionDurableResolver struct {
	ssh     *sshdaemon.Resolver
	events  ipc.Emitter
	helpers *productionHelperService
	// rules keeps every resolved helper on its owner's state rules.
	rules *stateRulesRuntime
}

func productionHelperProgressEvent(scope ipc.HelperProgressScope, progress sshdaemon.HelperProgress) ipc.HelperProgressEvent {
	event := ipc.HelperProgressEvent{
		SessionID: scope.SessionID, AssetID: scope.AssetID,
		OperationID: progress.OperationID, Attempt: progress.Attempt, Attempts: progress.Attempts,
		Step: progress.Step, Steps: progress.Steps, Stage: progress.Stage,
		Cause: progress.Cause, Path: progress.Path, Transport: progress.Transport,
		Transferred: progress.Transferred, Total: progress.Total, Percent: progress.Percent,
		Done: progress.Done, Error: progress.Error,
	}
	if !progress.Deadline.IsZero() {
		event.Deadline = progress.Deadline.UnixMilli()
	}
	return event
}

func (r productionDurableResolver) ResolveDurable(ctx context.Context, transport base.Transport) (base.DurableProvider, error) {
	provider, err := r.resolveDurable(ctx, transport)
	if err != nil {
		return nil, err
	}
	return r.rules.wrapProvider(ctx, ipc.HelperProgressScopeFromContext(ctx).AssetID, provider), nil
}

func (r productionDurableResolver) resolveDurable(ctx context.Context, transport base.Transport) (base.DurableProvider, error) {
	if deviceTransport, ok := transport.(*device.Transport); ok {
		return deviceTransport.Provider(), nil
	}
	if r.ssh != nil {
		// Resolver instances are shared by all sessions. Clone the small
		// configuration value so a concurrent connection cannot overwrite the
		// progress callback for another session.
		resolver := *r.ssh
		scope := ipc.HelperProgressScopeFromContext(ctx)
		if r.helpers != nil {
			if err := r.helpers.configureResolver(&resolver, scope.AssetID); err != nil {
				return nil, err
			}
		}
		deployStart := time.Now()
		lastStage := ""
		var stageMu sync.Mutex
		resolver.Progress = func(progressCtx context.Context, progress sshdaemon.HelperProgress) {
			session.ReportHelperOperation(progressCtx, progress.OperationID)
			// Debug capture aid: one line per stage change (not per transferred chunk); no paths of user data or content.
			stageMu.Lock()
			stageChanged := progress.Stage != lastStage || progress.Done || progress.Error != ""
			lastStage = progress.Stage
			stageMu.Unlock()
			if stageChanged {
				slog.Debug("helper deployment progress", "session", scope.SessionID, "operation", progress.OperationID,
					"stage", progress.Stage, "step", progress.Step, "steps", progress.Steps, "attempt", progress.Attempt,
					"done", progress.Done, "error", progress.Error, "elapsed_ms", time.Since(deployStart).Milliseconds())
			}
			if r.events != nil && scope.AssetID != "" {
				_ = ipc.Emit(progressCtx, r.events, ipc.TopicHelperProgress, productionHelperProgressEvent(scope, progress))
			}
		}
		return resolver.ResolveDurable(ctx, transport)
	}
	return nil, fmt.Errorf("%w: remote durable tabs need an SSH transport", base.ErrUnsupported)
}

type helperVersionDTO struct {
	Known    bool   `json:"known"`
	Release  string `json:"release,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Protocol *int   `json:"protocol,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type helperUpdateDTO struct {
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Updated     bool   `json:"updated"`
	Interrupted bool   `json:"interrupted"`
	RolledBack  bool   `json:"rolledBack"`
	// ReplacedLegacy reports that an older-protocol helper was ended with the
	// user's consent, with the EndedSessions they agreed to end.
	ReplacedLegacy bool `json:"replacedLegacy,omitempty"`
	// Stopped reports that this action ended the older helper; false when
	// it had already exited or another replacement ended it, so the
	// sessions were not ended by it. Meaningful with ReplacedLegacy.
	Stopped bool `json:"stopped,omitempty"`
	// EndedSessions is always sent with ReplacedLegacy, 0 included.
	EndedSessions int `json:"endedSessions"`
	// EndedCount is how exact EndedSessions is: "exact", "atLeast" (only
	// terminal sessions were counted) or "unknown".
	EndedCount string `json:"endedCount,omitempty"`
	// Forced reports that the older helper ignored TERM and was killed, so
	// processes it started may still run.
	Forced bool `json:"forced,omitempty"`
}

type helperOfflineDTO struct {
	FileName      string `json:"fileName"`
	SHA256        string `json:"sha256"`
	Protocol      *int   `json:"protocol,omitempty"`
	Compatibility string `json:"compatibility"`
	Trusted       bool   `json:"trusted"`
}

type helperOperationDTO struct {
	SessionID     string            `json:"sessionId"`
	OperationID   string            `json:"operationId,omitempty"`
	Action        string            `json:"action"`
	Accepted      bool              `json:"accepted"`
	Local         helperVersionDTO  `json:"local"`
	Running       helperVersionDTO  `json:"running"`
	Compatibility string            `json:"compatibility"`
	Update        *helperUpdateDTO  `json:"update,omitempty"`
	Offline       *helperOfflineDTO `json:"offline,omitempty"`
	// Legacy is an older-protocol helper that still owns the host's helper
	// state: what ending it would end, and the token to approve that.
	Legacy *session.LegacyHelperDetail `json:"legacy,omitempty"`
}

type trustedOfflineHelper struct {
	path   string
	sha256 string
}

type productionHelperService struct {
	ssh      *sshdaemon.Resolver
	sessions *session.Manager

	mu      sync.Mutex
	offline map[string]trustedOfflineHelper
}

func (s *productionHelperService) configureResolver(resolver *sshdaemon.Resolver, assetID string) error {
	s.mu.Lock()
	trusted, ok := s.offline[assetID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	source, err := sshdaemon.InspectExecutableContext(context.Background(), trusted.path)
	if err != nil {
		return fmt.Errorf("%w: revalidate offline helper: %v", sshdaemon.ErrTrust, err)
	}
	if !strings.EqualFold(source.Digest, trusted.sha256) {
		return fmt.Errorf("%w: offline helper content changed; confirm the new SHA256 before use", sshdaemon.ErrDigestMismatch)
	}
	protocol, known := offlineHelperProtocol(source)
	if !known || protocol != supervisor.ProtocolVersion {
		return fmt.Errorf("%w: offline helper protocol changed or is no longer provable", sshdaemon.ErrTrust)
	}
	resolver.HelperExecutable = trusted.path
	resolver.HelperSHA256 = trusted.sha256
	return nil
}

func (s *productionHelperService) resolver(assetID string) (*sshdaemon.Resolver, error) {
	if s.ssh == nil {
		return nil, fmt.Errorf("%w: no local helper resolver", base.ErrUnsupported)
	}
	resolver := *s.ssh
	resolver.Progress = nil
	if err := s.configureResolver(&resolver, assetID); err != nil {
		return nil, err
	}
	return &resolver, nil
}

func helperRuntimeVersion(metadata supervisor.RuntimeMetadata, known bool) helperVersionDTO {
	if !known {
		return helperVersionDTO{Known: false}
	}
	protocol := metadata.Protocol
	return helperVersionDTO{
		Known: true, Release: metadata.Version, Commit: metadata.Commit,
		Protocol: &protocol, SHA256: metadata.SHA256,
	}
}

func helperMetadataKnown(metadata supervisor.RuntimeMetadata) bool {
	return metadata.Version != "" || metadata.Commit != "" || metadata.Protocol != 0 || metadata.SHA256 != ""
}

func helperCompatibility(running helperVersionDTO) string {
	if !running.Known || running.Protocol == nil {
		return "unknown"
	}
	if *running.Protocol != supervisor.ProtocolVersion {
		return "incompatible"
	}
	return "compatible"
}

func helperDTOFromDaemon(sessionID, action string, daemon *sshdaemon.Daemon) helperOperationDTO {
	local := helperRuntimeVersion(daemon.Update.Local, helperMetadataKnown(daemon.Update.Local))
	running := helperRuntimeVersion(daemon.RunningMetadata, daemon.MetadataKnown)
	matchingContent := local.Known && running.Known && local.SHA256 != "" &&
		strings.EqualFold(local.SHA256, running.SHA256)
	if matchingContent {
		// Matching content identifies the same executable even when its local build info omits release metadata.
		if local.Release == "" {
			local.Release = running.Release
		}
		if local.Commit == "" {
			local.Commit = running.Commit
		}
	}
	update := &helperUpdateDTO{
		Status: "unknown", Updated: daemon.Update.Updated,
		Interrupted: daemon.Update.Interrupted, RolledBack: daemon.Update.RolledBack,
		ReplacedLegacy: daemon.Update.ReplacedLegacy, EndedSessions: daemon.Update.EndedSessions,
		EndedCount: daemon.Update.EndedCount, Stopped: daemon.Update.StoppedLegacy, Forced: daemon.Update.ForcedLegacy,
	}
	switch {
	case daemon.Update.ReplacedLegacy:
		update.Status = "replaced"
	case daemon.Update.RolledBack:
		update.Status = "rolled_back"
	case daemon.Update.Updated && daemon.Update.Interrupted:
		update.Status = "interrupted"
	case daemon.Update.Updated:
		update.Status = "updated"
	case daemon.Update.Interrupted:
		update.Status = "interrupted"
	case daemon.Update.Deferred:
		update.Status = "deferred"
	case matchingContent:
		update.Status = "current"
	case local.Known && local.SHA256 != "" && running.Known && running.SHA256 != "" &&
		local.Protocol != nil && running.Protocol != nil && *local.Protocol == *running.Protocol:
		update.Status = "available"
	}
	details := make([]string, 0, 2)
	if daemon.Update.Reason != "" {
		details = append(details, daemon.Update.Reason)
	}
	if daemon.Update.RolledBack {
		details = append(details, "已恢复为更新前的版本；更新时结束的进程不会恢复")
	}
	update.Detail = strings.Join(details, "；")
	return helperOperationDTO{
		SessionID: sessionID, Action: action,
		Local: local, Running: running, Compatibility: helperCompatibility(running), Update: update,
	}
}

// legacyHelperDTO reports an older helper that blocks durable terminals:
// what ending it would end and the token to approve that. It is a result,
// not an error, so the helper window can ask for consent.
func legacyHelperDTO(sessionID, action string, err error) (helperOperationDTO, bool) {
	detail := session.LegacyHelperDetailFor(err)
	if detail == nil {
		return helperOperationDTO{}, false
	}
	protocol := detail.Protocol
	running := helperVersionDTO{Known: detail.Release != "", Release: detail.Release, Protocol: &protocol}
	return helperOperationDTO{
		SessionID: sessionID, Action: action, Local: helperVersionDTO{Known: false}, Running: running,
		Compatibility: "incompatible", Update: &helperUpdateDTO{Status: "legacy"}, Legacy: detail,
	}, true
}

// replacedLegacy finishes a consented replacement: the tabs that ended with
// the legacy banner learn that their sessions were ended.
func (s *productionHelperService) replacedLegacy(ctx context.Context, sessionID string) {
	s.sessions.LegacyHelperReplaced(ctx, sessionID)
}

// replaceFailed tells those tabs as well when the older helper was ended
// before the replacement failed: their sessions are gone either way.
func (s *productionHelperService) replaceFailed(ctx context.Context, sessionID string, err error) {
	if failure := session.HelperReplaceFailureFor(err); failure != nil && failure.Stopped {
		s.replacedLegacy(ctx, sessionID)
	}
}

// ensureDevice reads or replaces the helper of a managed device through its
// terminal bridge; the device's agent acts only on the consent sent here.
func (s *productionHelperService) ensureDevice(ctx context.Context, sessionID, action, consentToken string, transport base.Transport) (helperOperationDTO, error) {
	provider, ok := transport.(interface {
		Provider() *supervisor.RemoteProvider
	})
	if !ok || provider.Provider() == nil {
		return helperOperationDTO{}, session.ErrUnsupported
	}
	var replaced supervisor.ReplaceResult
	switch action {
	case "status":
	case "replace_legacy":
		var err error
		replaced, err = provider.Provider().ReplaceLegacyHelper(ctx, consentToken)
		if err != nil {
			if result, ok := legacyHelperDTO(sessionID, action, err); ok {
				return result, nil
			}
			s.replaceFailed(ctx, sessionID, err)
			return helperOperationDTO{}, err
		}
		s.replacedLegacy(ctx, sessionID)
	default:
		return helperOperationDTO{}, session.ErrUnsupported
	}
	metadata, known, err := provider.Provider().HelperStatus(ctx)
	if err != nil {
		if result, ok := legacyHelperDTO(sessionID, action, err); ok {
			return result, nil
		}
		return helperOperationDTO{}, err
	}
	running := helperRuntimeVersion(metadata, known)
	result := helperOperationDTO{
		SessionID: sessionID, Action: action, Local: helperVersionDTO{Known: false}, Running: running,
		Compatibility: helperCompatibility(running), Update: &helperUpdateDTO{Status: "unknown"},
	}
	if action == "replace_legacy" {
		result.Update = replacedUpdateDTO(consentToken, replaced)
	}
	return result, nil
}

// replacedUpdateDTO describes a replacement the device bridge completed.
func replacedUpdateDTO(consentToken string, replaced supervisor.ReplaceResult) *helperUpdateDTO {
	update := &helperUpdateDTO{Status: "replaced", ReplacedLegacy: true, Stopped: replaced.Stopped, Forced: replaced.Forced}
	if consent, ok := supervisor.ParseHelperConsent(consentToken); ok {
		update.EndedSessions = consent.Activity.Total()
		update.EndedCount = sshdaemon.EndedCountOf(consent)
	}
	return update
}

func (s *productionHelperService) ensure(ctx context.Context, sessionID, action, consentToken string) (helperOperationDTO, error) {
	connected, err := s.sessions.SessionFor(ctx, sessionID)
	if err != nil {
		return helperOperationDTO{}, err
	}
	asset := connected.Asset()
	if asset.Kind != session.KindSSH && asset.Kind != session.KindDevice {
		return helperOperationDTO{}, session.ErrUnsupported
	}
	transport, err := s.sessions.TransportFor(ctx, sessionID)
	if err != nil {
		return helperOperationDTO{}, err
	}
	if asset.Kind == session.KindDevice {
		return s.ensureDevice(ctx, sessionID, action, consentToken, transport)
	}
	client, ok := transport.(*transportssh.Client)
	if !ok {
		return helperOperationDTO{}, session.ErrUnsupported
	}
	resolver, err := s.resolver(asset.ID)
	if err != nil {
		return helperOperationDTO{}, err
	}
	if action == "status" {
		resolver.DisableAutoInstall = true
	}
	options := sshdaemon.EnsureOptions{InterruptActive: action == "update_interrupt"}
	if action == "replace_legacy" {
		options.ReplaceLegacy = consentToken
	}
	daemon, err := resolver.EnsureDaemonWithOptions(ctx, client, options)
	if daemon == nil {
		if result, ok := legacyHelperDTO(sessionID, action, err); ok {
			return result, nil
		}
		s.replaceFailed(ctx, sessionID, err)
		return helperOperationDTO{}, err
	}
	if daemon.Update.ReplacedLegacy {
		s.replacedLegacy(ctx, sessionID)
	}
	return helperDTOFromDaemon(sessionID, action, daemon), err
}

func helperManifestArchitecture(source sshdaemon.UploadSource) string {
	if source.GOARCH == "arm" && source.GOARM == "7" {
		return "armv7"
	}
	return source.GOARCH
}

func offlineHelperProtocol(source sshdaemon.UploadSource) (int, bool) {
	raw, err := os.ReadFile(source.Path + ".manifest.json")
	if err != nil {
		return 0, false
	}
	var manifest struct {
		BinaryMetadataVerified bool   `json:"binary_metadata_verified"`
		Protocol               *int   `json:"protocol"`
		SHA256                 string `json:"sha256"`
		MetadataHost           struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
		} `json:"metadata_host"`
		Artifact struct {
			SHA256 string `json:"sha256"`
		} `json:"artifact"`
	}
	if json.Unmarshal(raw, &manifest) != nil || !manifest.BinaryMetadataVerified || manifest.Protocol == nil || *manifest.Protocol <= 0 {
		return 0, false
	}
	if !strings.EqualFold(manifest.SHA256, source.Digest) || !strings.EqualFold(manifest.Artifact.SHA256, source.Digest) {
		return 0, false
	}
	if manifest.MetadataHost.OS != source.GOOS || manifest.MetadataHost.Arch != helperManifestArchitecture(source) {
		return 0, false
	}
	return *manifest.Protocol, true
}

func (s *productionHelperService) inspectOffline(ctx context.Context, sessionID, path, expectedSHA string, confirmed bool) (helperOperationDTO, error) {
	connected, err := s.sessions.SessionFor(ctx, sessionID)
	if err != nil {
		return helperOperationDTO{}, err
	}
	asset := connected.Asset()
	if asset.Kind != session.KindSSH || strings.TrimSpace(path) == "" {
		return helperOperationDTO{}, session.ErrInvalidOptions
	}
	source, err := sshdaemon.InspectExecutableContext(ctx, path)
	if err != nil {
		return helperOperationDTO{}, err
	}
	protocol, protocolKnown := offlineHelperProtocol(source)
	compatibility := "unknown"
	if protocolKnown {
		if protocol == supervisor.ProtocolVersion {
			compatibility = "compatible"
		} else {
			compatibility = "incompatible"
		}
	}
	local := helperVersionDTO{Known: true, Release: source.Version, Commit: source.Commit, SHA256: source.Digest}
	if protocolKnown {
		local.Protocol = &protocol
	}
	result := helperOperationDTO{
		SessionID: sessionID, Action: "offline", Local: local, Compatibility: compatibility,
		Offline: &helperOfflineDTO{
			FileName: filepath.Base(path), SHA256: source.Digest, Compatibility: compatibility,
		},
	}
	if protocolKnown {
		result.Offline.Protocol = &protocol
	}
	if !confirmed {
		return result, nil
	}
	if compatibility != "compatible" {
		return helperOperationDTO{}, fmt.Errorf("%w: offline helper protocol is unknown or incompatible", sshdaemon.ErrTrust)
	}
	if len(expectedSHA) != len(source.Digest) || !strings.EqualFold(expectedSHA, source.Digest) {
		return helperOperationDTO{}, fmt.Errorf("%w: confirmed SHA256 does not match the offline file", sshdaemon.ErrDigestMismatch)
	}
	s.mu.Lock()
	if s.offline == nil {
		s.offline = make(map[string]trustedOfflineHelper)
	}
	s.offline[asset.ID] = trustedOfflineHelper{path: path, sha256: source.Digest}
	s.mu.Unlock()
	result.Offline.Trusted = true
	result.Accepted = true
	return result, nil
}
