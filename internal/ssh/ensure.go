package ssh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

const helperProgressSteps = 6

type targetInfo struct {
	dataRoot string
	goos     string
	goarch   string
	goarm    string
	euid     string
}

type helperOperationScope struct {
	id       string
	attempt  int
	deadline time.Time
}

type helperOperationKey struct{}

type remoteDaemonClient interface {
	List(context.Context) ([]supervisor.Info, error)
	RuntimeMetadata(context.Context) (supervisor.RuntimeMetadata, bool, error)
	RequireIdle(context.Context) error
}

type daemonConnection struct {
	daemon   *Daemon
	metadata supervisor.RuntimeMetadata
	known    bool
	running  bool
}

type deploymentLocation struct {
	target    Target
	paths     TargetPaths
	temporary bool
	base      string
}

var (
	errHelperProcessNotFound  = errors.New("actual helper process not found")
	errRunningProcessEvidence = errors.New("running helper process evidence unavailable")
)

type helperProcess struct {
	pid    int
	binary string
}

func (r *Resolver) ensureOnHost(ctx context.Context, host Host) (*Daemon, error) {
	return r.ensureOnHostWithClient(ctx, host, nil, EnsureOptions{})
}

func (r *Resolver) ensureOnHostWithProgress(ctx context.Context, host Host) (*Daemon, error) {
	return r.ensureOnHostWithClient(ctx, host, nil, EnsureOptions{})
}

func (r *Resolver) reportProgress(ctx context.Context, progress HelperProgress) {
	if r.Progress == nil {
		return
	}
	if scope, ok := ctx.Value(helperOperationKey{}).(helperOperationScope); ok {
		progress.OperationID = scope.id
		progress.Attempt = scope.attempt
		progress.Attempts = maxEnsureAttempts
		progress.Deadline = scope.deadline
	}
	if progress.Steps <= 0 {
		progress.Steps = helperProgressSteps
	}
	if progress.Cause == "" {
		progress.Cause = progress.Error
	}
	if progress.Error == "" && (progress.Stage == "failed" || progress.Stage == "backoff" || progress.Stage == "rollback") {
		progress.Error = progress.Cause
	}
	progress.Percent = -1
	if progress.Total > 0 {
		progress.Percent = int(progress.Transferred * 100 / progress.Total)
		if progress.Percent < 0 {
			progress.Percent = 0
		} else if progress.Percent > 100 {
			progress.Percent = 100
		}
	}
	if progress.Done && progress.Error == "" {
		progress.Percent = 100
	}
	r.Progress(ctx, progress)
}

func (r *Resolver) ensureOnHostWithClient(ctx context.Context, host Host, client *transportssh.Client, options EnsureOptions) (*Daemon, error) {
	timeout := r.EnsureTimeout
	if timeout <= 0 {
		timeout = defaultEnsureTimeout
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := operationCtx.Deadline()
	operationID, err := newHelperOperationID()
	if err != nil {
		return nil, fmt.Errorf("%w: allocate helper operation identity: %v", durable.ErrUnavailable, err)
	}
	var lastErr error
	for attempt := 1; attempt <= maxEnsureAttempts; attempt++ {
		attemptCtx := context.WithValue(operationCtx, helperOperationKey{}, helperOperationScope{id: operationID, attempt: attempt, deadline: deadline})
		daemon, cycleErr := r.ensureCycle(attemptCtx, host, client, options)
		if cycleErr == nil {
			r.reportProgress(attemptCtx, HelperProgress{Step: helperProgressSteps, Stage: "ready", Done: true, Path: daemon.StateDir})
			return daemon, nil
		}
		lastErr = cycleErr
		if operationCtx.Err() != nil || attempt == maxEnsureAttempts || permanentHelperFailure(cycleErr) {
			r.reportProgress(attemptCtx, HelperProgress{Step: helperProgressSteps, Stage: "failed", Done: true, Cause: cycleErr.Error()})
			return daemon, lastErr
		}
		r.reportProgress(attemptCtx, HelperProgress{Step: helperProgressSteps, Stage: "backoff", Cause: cycleErr.Error()})
		backoff := r.RetryBackoff
		if backoff < 0 {
			backoff = 0
		} else if backoff == 0 {
			backoff = defaultRetryBackoff
		}
		backoff *= time.Duration(attempt)
		timer := time.NewTimer(backoff)
		select {
		case <-operationCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			cause := context.Cause(operationCtx)
			r.reportProgress(attemptCtx, HelperProgress{Step: helperProgressSteps, Stage: "failed", Done: true, Cause: cause.Error()})
			return nil, cause
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func newHelperOperationID() (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "helper-" + hex.EncodeToString(nonce[:]), nil
}

func permanentHelperFailure(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, base.ErrUnsupported) || errors.Is(err, ErrHelperNotFound) ||
		errors.Is(err, ErrTrust) || errors.Is(err, ErrDigestMismatch) || errors.Is(err, ErrNoExec) ||
		errors.Is(err, ErrProtocol) || errors.Is(err, supervisor.ErrProtocol) ||
		errors.Is(err, supervisor.ErrStateMismatch) || errors.Is(err, fs.ErrPermission)
}

func (r *Resolver) ensureCycle(ctx context.Context, host Host, client *transportssh.Client, options EnsureOptions) (*Daemon, error) {
	r.reportProgress(ctx, HelperProgress{Step: 1, Stage: "discover"})
	target, err := DiscoverTarget(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, helperUnavailable("target discovery", err)
	}
	if client != nil && r.hostFor == nil {
		execHost, err := NewExecHost(client, target)
		if err != nil {
			return nil, err
		}
		host = NewFallbackHost(host, execHost)
	}
	primaryPaths, err := target.Paths()
	if err != nil {
		return nil, err
	}
	locations := []deploymentLocation{{target: target, paths: primaryPaths}}
	locations = append(locations, discoverFallbackLocations(ctx, host, target)...)

	source, sourceErr := r.uploadSourceForTarget(target)
	ordered, err := orderDeploymentLocations(ctx, host, locations)
	if err != nil {
		return nil, err
	}

	for _, location := range ordered {
		connection, err := r.findRunningDaemon(ctx, host, location, source)
		if err != nil {
			if daemonRefusal(err) || errors.Is(err, errRunningProcessEvidence) {
				return nil, err
			}
			if errors.Is(err, errUnsafeTemporaryRoot) {
				if len(ordered) > 1 {
					continue
				}
				return nil, err
			}
			continue
		}
		if connection == nil {
			continue
		}
		connection.daemon.Temporary = location.temporary
		if source != nil {
			connection.daemon.Update.Local = localHelperMetadata(*source)
			connection.daemon.Update.Running = connection.metadata
		}
		if sourceErr == nil && source != nil && connection.known && !r.DisableAutoInstall &&
			!strings.EqualFold(connection.metadata.SHA256, source.Digest) {
			return r.updateRunningDaemon(ctx, host, location, source, *connection, options)
		}
		if sourceErr != nil && !errors.Is(sourceErr, ErrHelperNotFound) {
			connection.daemon.Update.Reason = "compatible daemon retained; local update evidence is unavailable: " + sourceErr.Error()
		}
		return connection.daemon, nil
	}
	var lastErr error
	for _, location := range ordered {
		if err := r.validateDeploymentLocation(ctx, host, location); err != nil {
			lastErr = err
			if len(ordered) > 1 && deploymentPathUnavailable(err) {
				continue
			}
			return nil, err
		}
		for _, binary := range lookupPathBinaries(ctx, host, location.target) {
			r.reportProgress(ctx, HelperProgress{Step: 2, Stage: "probe", Path: binary})
			if err := probeTargetBinary(ctx, host, location.target, binary); err != nil {
				lastErr = err
				continue
			}
			connection, err := r.connectOrStartDaemon(ctx, host, location, binary, nil)
			if err == nil {
				connection.daemon.Temporary = location.temporary
				return daemonWithSource(connection, source), nil
			}
			lastErr = err
			if daemonRefusal(err) || !deploymentPathUnavailable(err) {
				return nil, err
			}
			break
		}
		if sourceErr != nil || source == nil {
			if r.DisableAutoInstall {
				return nil, fmt.Errorf("%w: no compatible helper is installed and automatic helper installation is disabled", durable.ErrUnavailable)
			}
			if sourceErr != nil {
				return nil, sourceErr
			}
			return nil, fmt.Errorf("%w: %w for %s", durable.ErrUnavailable, ErrHelperNotFound, target.ID())
		}
		if err := source.ValidateForTarget(location.target); err != nil {
			return nil, err
		}
		candidate, err := location.paths.Helper(source.Digest)
		if err != nil {
			return nil, err
		}
		if r.DisableAutoInstall {
			ready, err := cachedTargetBinary(ctx, host, location.target, candidate, source.Digest)
			if err != nil {
				return nil, err
			}
			if !ready {
				lastErr = fmt.Errorf("%w: no compatible helper is installed and automatic helper installation is disabled", durable.ErrUnavailable)
				if len(ordered) > 1 {
					continue
				}
				return nil, lastErr
			}
			connection, err := r.connectOrStartDaemon(ctx, host, location, candidate, nil)
			if err != nil {
				return nil, err
			}
			connection.daemon.Temporary = location.temporary
			return daemonWithSource(connection, source), nil
		}
		binary, err := r.installTargetHelper(ctx, host, location, *source)
		if err == nil {
			var connection *daemonConnection
			connection, err = r.connectOrStartDaemon(ctx, host, location, binary, nil)
			if err == nil {
				connection.daemon.Temporary = location.temporary
				return daemonWithSource(connection, source), nil
			}
		}
		lastErr = err
		if len(ordered) > 1 && deploymentPathUnavailable(err) {
			continue
		}
		return nil, err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%w: no usable helper deployment location", durable.ErrUnavailable)
}

func daemonWithSource(connection *daemonConnection, source *UploadSource) *Daemon {
	if source != nil {
		connection.daemon.Update.Local = localHelperMetadata(*source)
	}
	connection.daemon.Update.Running = connection.metadata
	return connection.daemon
}

func helperUnavailable(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, base.ErrUnsupported) {
		return err
	}
	return fmt.Errorf("%w: %s: %w", durable.ErrUnavailable, action, err)
}

func (r *Resolver) uploadSourceForTarget(target Target) (*UploadSource, error) {
	inspect := r.inspect
	if inspect == nil {
		inspect = InspectExecutable
	}
	if r.HelperExecutable != "" {
		source, err := inspect(r.HelperExecutable)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%w: %w: %s", durable.ErrUnavailable, ErrHelperNotFound, r.HelperExecutable)
			}
			return nil, fmt.Errorf("inspect offline helper %s: %w", r.HelperExecutable, err)
		}
		if r.HelperSHA256 != "" && !strings.EqualFold(source.Digest, r.HelperSHA256) {
			return nil, fmt.Errorf("%w: offline helper digest %s does not match confirmed SHA256 %s", ErrDigestMismatch, source.Digest, r.HelperSHA256)
		}
		return &source, nil
	}
	if r.Executable == "" || !strings.HasPrefix(strings.ToLower(filepath.Base(r.Executable)), "nexterm") {
		return nil, fmt.Errorf("%w: %w for %s; configure a standalone offline helper", durable.ErrUnavailable, ErrHelperNotFound, target.ID())
	}
	name := "nexterm-helper-" + target.ID()
	if target.GOOS == "windows" {
		name += ".exe"
	}
	candidate := filepath.Join(filepath.Dir(r.Executable), name)
	source, err := inspect(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %w: %s", durable.ErrUnavailable, ErrHelperNotFound, candidate)
		}
		return nil, fmt.Errorf("inspect target helper %s: %w", candidate, err)
	}
	if filepath.Clean(source.Path) != filepath.Clean(candidate) {
		return nil, fmt.Errorf("%w: inspected helper path %s does not match %s", ErrTrust, source.Path, candidate)
	}
	return &source, nil
}

func (r *Resolver) uploadSource(info targetInfo) (*UploadSource, bool, error) {
	source, err := r.uploadSourceForTarget(Target{GOOS: info.goos, GOARCH: info.goarch, GOARM: info.goarm, DataRoot: info.dataRoot, Identity: info.euid})
	return source, true, err
}

func localHelperMetadata(source UploadSource) supervisor.RuntimeMetadata {
	return supervisor.RuntimeMetadata{
		Version: source.Version, Commit: source.Commit, Protocol: supervisor.ProtocolVersion, SHA256: strings.ToLower(source.Digest),
	}
}

func discoverFallbackLocations(ctx context.Context, host Host, target Target) []deploymentLocation {
	var command string
	if target.GOOS == "windows" {
		command = powerShellCommand("powershell.exe", "[Console]::Out.Write([IO.Path]::GetTempPath())")
	} else {
		command = "printf '%s\\n' \"${XDG_RUNTIME_DIR:-}\" \"${TMPDIR:-}\""
	}
	stdout, exitCode, err := host.Run(ctx, command, probeTimeout)
	if err != nil || exitCode != 0 {
		return nil
	}
	var roots []deploymentLocation
	add := func(dataRoot, baseDir string) {
		if dataRoot == "" || strings.ContainsRune(dataRoot, '\x00') {
			return
		}
		candidate := target
		candidate.DataRoot = strings.TrimRight(dataRoot, "/\\")
		if candidate.DataRoot == "" {
			return
		}
		paths, err := candidate.Paths()
		if err != nil {
			return
		}
		for _, existing := range roots {
			if existing.paths.Root == paths.Root {
				return
			}
		}
		roots = append(roots, deploymentLocation{target: candidate, paths: paths, temporary: true, base: baseDir})
	}
	if target.GOOS == "windows" {
		temporary := strings.TrimSpace(stdout)
		add(temporary, temporary)
		return roots
	}
	lines := strings.Split(stdout, "\n")
	if len(lines) > 0 {
		xdg := strings.TrimSpace(lines[0])
		add(xdg, xdg)
	}
	if target.GOOS == "darwin" {
		if len(lines) > 1 {
			temporary := strings.TrimSpace(lines[1])
			add(temporary, temporary)
		}
	} else {
		baseDir := "/tmp/nexterm-" + target.Identity
		add(baseDir, baseDir)
	}
	return roots
}

func orderDeploymentLocations(ctx context.Context, host Host, locations []deploymentLocation) ([]deploymentLocation, error) {
	if len(locations) == 0 {
		return nil, fmt.Errorf("%w: no helper deployment location", durable.ErrUnavailable)
	}
	for _, location := range locations {
		if location.temporary {
			if err := validateNexTermPaths(ctx, host, location); err != nil {
				return nil, err
			}
		}
		exists, err := host.Exists(ctx, location.paths.StateDir)
		if err != nil {
			return nil, helperUnavailable("inspect existing daemon state "+location.paths.StateDir, err)
		}
		if exists {
			if location.temporary {
				if err := validateFallbackBase(ctx, host, location); err != nil {
					return nil, err
				}
			}
			return []deploymentLocation{location}, nil
		}
	}
	return locations, nil
}

func lookupPathBinaries(ctx context.Context, host Host, target Target) []string {
	command := "command -v nexterm-helper 2>/dev/null; true"
	if target.GOOS == "windows" {
		command = powerShellCommand("powershell.exe", "$command=Get-Command nexterm-helper.exe -ErrorAction SilentlyContinue; if ($command) { [Console]::Out.Write($command.Source) }")
	}
	stdout, _, err := host.Run(ctx, command, probeTimeout)
	if err != nil {
		return nil
	}
	return nonEmptyLines(stdout)
}

func (r *Resolver) findRunningDaemon(ctx context.Context, host Host, location deploymentLocation, source *UploadSource) (*daemonConnection, error) {
	if location.temporary {
		baseExists, err := host.Exists(ctx, location.base)
		if err != nil {
			return nil, unsafeTemporaryError(err)
		}
		stateExists, err := host.Exists(ctx, location.paths.StateDir)
		if err != nil {
			return nil, unsafeTemporaryError(err)
		}
		if baseExists || stateExists {
			if err := r.validateDeploymentLocation(ctx, host, location); err != nil {
				return nil, err
			}
		}
	}
	binaries := make([]string, 0, 2)
	process, err := inspectHelperProcess(ctx, host, location.target, location.paths.StateDir)
	if err == nil {
		binaries = append(binaries, process.binary)
	} else if !errors.Is(err, errHelperProcessNotFound) {
		return nil, fmt.Errorf("%w: %w", errRunningProcessEvidence, err)
	}
	binaries = append(binaries, lookupPathBinaries(ctx, host, location.target)...)
	if source != nil && source.ValidateForTarget(location.target) == nil {
		if cached, err := location.paths.Helper(source.Digest); err == nil {
			exists, existsErr := host.Exists(ctx, cached)
			if existsErr != nil {
				return nil, helperUnavailable("inspect cached helper", existsErr)
			}
			if exists {
				binaries = append(binaries, cached)
			}
		}
	}
	seen := make(map[string]bool)
	var lastErr error
	for _, binary := range binaries {
		if seen[binary] {
			continue
		}
		seen[binary] = true
		r.reportProgress(ctx, HelperProgress{Step: 2, Stage: "probe", Path: binary})
		if err := probeTargetBinary(ctx, host, location.target, binary); err != nil {
			lastErr = err
			continue
		}
		if source != nil && strings.Contains(binary, strings.ToLower(source.Digest)) {
			if err := verifyTargetBinary(ctx, host, location.target, binary, source.Digest); err != nil {
				lastErr = err
				continue
			}
		}
		daemon := newTargetDaemon(host, location, binary)
		if err := r.probeDaemon(ctx, daemon); err != nil {
			if daemonRefusal(err) {
				return nil, fmt.Errorf("%w: running supervisor daemon is incompatible or owns different state; refusing to replace it: %w", durable.ErrUnavailable, err)
			}
			lastErr = err
			continue
		}
		metadata, known, err := r.remoteClient(daemon).RuntimeMetadata(ctx)
		if err != nil {
			return nil, helperUnavailable("read running helper metadata", err)
		}
		daemon.RunningMetadata, daemon.MetadataKnown = metadata, known
		return &daemonConnection{daemon: daemon, metadata: metadata, known: known, running: true}, nil
	}
	return nil, lastErr
}

func newTargetDaemon(host Host, location deploymentLocation, binary string) *Daemon {
	digest, _ := supervisor.StateDigest(location.paths.StateDir, location.target.Identity)
	return &Daemon{host: host, Binary: binary, StateDir: location.paths.StateDir, Digest: digest, Target: location.target, Temporary: location.temporary}
}

func (r *Resolver) remoteClient(daemon *Daemon) remoteDaemonClient {
	if r.clientFor != nil {
		return r.clientFor(daemon)
	}
	return daemon.Client()
}

func (r *Resolver) probeDaemon(ctx context.Context, daemon *Daemon) error {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	_, err := r.remoteClient(daemon).List(probeCtx)
	return err
}

func daemonRefusal(err error) bool {
	return errors.Is(err, supervisor.ErrProtocol) || errors.Is(err, supervisor.ErrVersionMismatch) || errors.Is(err, supervisor.ErrStateMismatch)
}

func (r *Resolver) connectOrStartDaemon(ctx context.Context, host Host, location deploymentLocation, binary string, expected *supervisor.RuntimeMetadata) (*daemonConnection, error) {
	if err := r.validateDeploymentLocation(ctx, host, location); err != nil {
		return nil, err
	}
	daemon := newTargetDaemon(host, location, binary)
	if err := r.probeDaemon(ctx, daemon); err == nil {
		metadata, known, err := r.remoteClient(daemon).RuntimeMetadata(ctx)
		if err != nil {
			return nil, helperUnavailable("read running helper metadata", err)
		}
		if err := verifyExpectedMetadata(metadata, known, expected); err != nil {
			return nil, err
		}
		daemon.RunningMetadata, daemon.MetadataKnown = metadata, known
		return &daemonConnection{daemon: daemon, metadata: metadata, known: known, running: true}, nil
	} else if daemonRefusal(err) {
		return nil, fmt.Errorf("%w: running supervisor daemon is incompatible or owns different state; refusing to replace it: %w", durable.ErrUnavailable, err)
	}
	if err := ensureRemoteDirectory(ctx, host, location.target, location.paths.StateDir, true); err != nil {
		return nil, err
	}
	r.reportProgress(ctx, HelperProgress{Step: 5, Stage: "start", Path: binary})
	if err := spawnTargetDaemon(ctx, host, location.target, binary, location.paths.StateDir); err != nil {
		return nil, err
	}
	timeout := r.StartTimeout
	if timeout <= 0 {
		timeout = defaultStartTimeout
	}
	startCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		if err := r.probeDaemon(startCtx, daemon); err == nil {
			metadata, known, metadataErr := r.remoteClient(daemon).RuntimeMetadata(startCtx)
			if metadataErr != nil {
				lastErr = metadataErr
			} else if expectedErr := verifyExpectedMetadata(metadata, known, expected); expectedErr != nil {
				return nil, expectedErr
			} else {
				daemon.RunningMetadata, daemon.MetadataKnown = metadata, known
				return &daemonConnection{daemon: daemon, metadata: metadata, known: known}, nil
			}
		} else {
			if daemonRefusal(err) {
				return nil, fmt.Errorf("%w: spawned supervisor daemon cannot serve this client; refusing to replace it: %w", durable.ErrUnavailable, err)
			}
			lastErr = err
		}
		if startCtx.Err() != nil {
			if ctx.Err() != nil {
				return nil, context.Cause(ctx)
			}
			return nil, fmt.Errorf("%w: supervisor daemon did not become ready: %v", durable.ErrUnavailable, lastErr)
		}
		select {
		case <-startCtx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func verifyExpectedMetadata(metadata supervisor.RuntimeMetadata, known bool, expected *supervisor.RuntimeMetadata) error {
	if expected == nil {
		return nil
	}
	if !known {
		return fmt.Errorf("%w: started helper did not prove runtime metadata", ErrProtocol)
	}
	if metadata.Protocol != expected.Protocol || !strings.EqualFold(metadata.SHA256, expected.SHA256) ||
		expected.Version != "" && metadata.Version != expected.Version || expected.Commit != "" && metadata.Commit != expected.Commit {
		return fmt.Errorf("%w: started helper metadata does not match the staged binary", ErrProtocol)
	}
	return nil
}

func (r *Resolver) startOrConnect(ctx context.Context, host Host, binary, stateDir, digest string) (*Daemon, error) {
	daemon := &Daemon{host: host, Binary: binary, StateDir: stateDir, Digest: digest, Target: Target{GOOS: "linux", GOARCH: "amd64"}}
	if err := r.probeDaemon(ctx, daemon); err == nil {
		return daemon, nil
	} else if daemonRefusal(err) {
		return nil, fmt.Errorf("%w: running supervisor daemon cannot serve this client: %w", durable.ErrUnavailable, err)
	}
	if err := spawnDaemon(ctx, host, binary, stateDir); err != nil {
		return nil, err
	}
	timeout := r.StartTimeout
	if timeout <= 0 {
		timeout = defaultStartTimeout
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := r.probeDaemon(ctx, daemon); err == nil {
			return daemon, nil
		} else if daemonRefusal(err) {
			return nil, fmt.Errorf("%w: spawned supervisor daemon cannot serve this client: %w", durable.ErrUnavailable, err)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("%w: supervisor daemon did not become ready: %v", durable.ErrUnavailable, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func spawnTargetDaemon(ctx context.Context, host Host, target Target, binary, stateDir string) error {
	command, err := target.LaunchCommand(binary, stateDir)
	if err != nil {
		return err
	}
	if _, exitCode, err := host.Run(ctx, command, probeTimeout); err != nil {
		return helperUnavailable("spawn supervisor daemon", err)
	} else if exitCode != 0 {
		return fmt.Errorf("%w: prepare supervisor daemon exited with %d", durable.ErrUnavailable, exitCode)
	}
	return nil
}

func spawnDaemon(ctx context.Context, host Host, binary, stateDir string) error {
	command := "mkdir -p -- " + quoteShell(stateDir) + " && chmod 700 " + quoteShell(stateDir) +
		" && { setsid " + quoteShell(binary) + " " + supervisor.HelperCommand + " --state-dir " + quoteShell(stateDir) +
		" >>" + quoteShell(stateDir+"/helper.log") + " 2>&1 </dev/null & }"
	if _, exitCode, err := host.Run(ctx, command, probeTimeout); err != nil {
		return helperUnavailable("spawn supervisor daemon", err)
	} else if exitCode != 0 {
		return fmt.Errorf("%w: prepare supervisor daemon exited with %d", durable.ErrUnavailable, exitCode)
	}
	return nil
}

func verifyTargetBinary(ctx context.Context, host Host, target Target, binary, expectedDigest string) error {
	actualDigest, err := host.ChecksumSHA256(ctx, binary)
	if err != nil {
		return helperUnavailable("digest cached helper "+binary, err)
	}
	if !strings.EqualFold(actualDigest, expectedDigest) {
		return fmt.Errorf("%w: cached helper %s", ErrDigestMismatch, binary)
	}
	return probeTargetBinary(ctx, host, target, binary)
}

func verifyRemoteBinary(ctx context.Context, host Host, binary, expectedDigest string) error {
	return verifyTargetBinary(ctx, host, Target{GOOS: "linux", GOARCH: "amd64"}, binary, expectedDigest)
}

func probeTargetBinary(ctx context.Context, host Host, target Target, binary string) error {
	command, err := target.probeCommand(binary)
	if err != nil {
		return err
	}
	stdout, exitCode, err := host.Run(ctx, command, probeTimeout)
	if err != nil {
		return helperUnavailable("execute helper probe "+binary, err)
	}
	if exitCode != 0 {
		return transferError(TransferStageProbe, binary, remoteCommandFailure(exitCode, "", stdout))
	}
	if actual := strings.TrimSpace(stdout); actual != strconv.Itoa(supervisor.ProtocolVersion) {
		return fmt.Errorf("%w: helper probe %s reported protocol %q, want %d", ErrProtocol, binary, actual, supervisor.ProtocolVersion)
	}
	return nil
}

func probeBinary(ctx context.Context, host Host, binary string) error {
	return probeTargetBinary(ctx, host, Target{GOOS: "linux", GOARCH: "amd64"}, binary)
}

func cachedTargetBinary(ctx context.Context, host Host, target Target, binary, digest string) (bool, error) {
	exists, err := host.Exists(ctx, binary)
	if err != nil {
		return false, helperUnavailable("check cached helper "+binary, err)
	}
	if !exists {
		return false, nil
	}
	if err := verifyTargetBinary(ctx, host, target, binary, digest); err != nil {
		return true, err
	}
	return true, nil
}

func (r *Resolver) installTargetHelper(ctx context.Context, host Host, location deploymentLocation, source UploadSource) (string, error) {
	if err := r.validateDeploymentLocation(ctx, host, location); err != nil {
		return "", err
	}
	if err := ensureTargetDirectories(ctx, host, location.target, location.paths); err != nil {
		return "", err
	}
	uploadCtx, cancel := context.WithTimeout(ctx, r.uploadTimeout())
	defer cancel()
	r.reportProgress(uploadCtx, HelperProgress{Step: 3, Stage: "cache", Path: location.paths.BinDir})
	binary, err := StageUpload(uploadCtx, host, location.target, source, true, r.Progress)
	if err != nil {
		return binary, helperUnavailable("install target helper", err)
	}
	return binary, nil
}

func (r *Resolver) uploadTimeout() time.Duration {
	if r.UploadTimeout > 0 {
		return r.UploadTimeout
	}
	return defaultUploadTimeout
}

func (r *Resolver) updateRunningDaemon(ctx context.Context, host Host, location deploymentLocation, source *UploadSource, current daemonConnection, options EnsureOptions) (result *Daemon, retErr error) {
	local := localHelperMetadata(*source)
	current.daemon.Update.Local = local
	current.daemon.Update.Running = current.metadata
	if !current.known {
		current.daemon.Update.Deferred = true
		current.daemon.Update.Reason = "running helper version is not provable; compatible daemon retained"
		return current.daemon, nil
	}
	r.reportProgress(ctx, HelperProgress{Step: 4, Stage: "install_update", Path: location.paths.BinDir})
	candidate, err := r.installTargetHelper(ctx, host, location, *source)
	if err != nil {
		return nil, err
	}
	lock := remoteTargetJoin(location.target, location.paths.Root, ".helper-update")
	if err := host.Mkdir(ctx, lock); err != nil {
		if errors.Is(err, fs.ErrExist) {
			current.daemon.Update.Deferred = true
			current.daemon.Update.Reason = "another helper update is in progress"
			return current.daemon, nil
		}
		return nil, helperUnavailable("lock helper update", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		if err := host.Delete(cleanupCtx, lock, true); err != nil && !errors.Is(err, fs.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("%w: remove helper update lock %s: %v", durable.ErrUnavailable, lock, err))
		}
	}()

	deferUpdate := func(reason string) (*Daemon, error) {
		current.daemon.Update.Deferred = true
		current.daemon.Update.Reason = reason
		r.reportProgress(ctx, HelperProgress{Step: 5, Stage: "deferred", Cause: reason, Path: current.daemon.StateDir})
		return current.daemon, nil
	}
	if err := r.recheckUpdateDaemon(ctx, current.daemon, current.metadata); err != nil {
		return nil, err
	}
	idle, reason, err := r.updateIdleAllowed(ctx, current.daemon, options.InterruptActive)
	if err != nil {
		return nil, err
	}
	if !idle {
		return deferUpdate(reason)
	}
	process, err := inspectHelperProcess(ctx, host, location.target, location.paths.StateDir)
	if err != nil {
		return deferUpdate("actual running helper process and rollback path are not provable: " + err.Error())
	}
	oldDigest, err := host.ChecksumSHA256(ctx, process.binary)
	if err != nil || !strings.EqualFold(oldDigest, current.metadata.SHA256) {
		if err == nil {
			err = fmt.Errorf("actual executable digest %s does not match running metadata %s", oldDigest, current.metadata.SHA256)
		}
		return deferUpdate("actual running executable is not preserved and verifiable: " + err.Error())
	}
	if err := r.recheckUpdateDaemon(ctx, current.daemon, current.metadata); err != nil {
		return nil, err
	}
	idle, reason, err = r.updateIdleAllowed(ctx, current.daemon, options.InterruptActive)
	if err != nil {
		return nil, err
	}
	if !idle {
		return deferUpdate(reason)
	}
	current.daemon.Update.Interrupted = options.InterruptActive && strings.Contains(reason, "active")
	r.reportProgress(ctx, HelperProgress{Step: 5, Stage: "switch", Path: candidate})
	if err := stopHelperProcess(ctx, host, location.target, process); err != nil {
		return nil, err
	}
	if err := r.waitDaemonStopped(ctx, current.daemon); err != nil {
		return nil, err
	}
	updated, updateErr := r.connectOrStartDaemon(ctx, host, location, candidate, &local)
	if updateErr == nil {
		updated.daemon.Update = current.daemon.Update
		updated.daemon.Update.Updated = true
		updated.daemon.Update.Local = local
		updated.daemon.Update.Running = current.metadata
		r.reportProgress(ctx, HelperProgress{Step: 6, Stage: "updated", Path: candidate})
		return updated.daemon, nil
	}

	r.reportProgress(ctx, HelperProgress{Step: 6, Stage: "rollback", Cause: updateErr.Error(), Path: process.binary})
	rollbackTimeout := r.StartTimeout
	if rollbackTimeout <= 0 {
		rollbackTimeout = defaultStartTimeout
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	rollbackErr := r.rollbackDaemon(rollbackCtx, host, location, candidate, process.binary, local, current.metadata)
	if rollbackErr != nil {
		return nil, errors.Join(fmt.Errorf("helper update failed: %w", updateErr), fmt.Errorf("helper rollback failed; interrupted shells were not revived: %w", rollbackErr))
	}
	current.daemon.Update.RolledBack = true
	return current.daemon, fmt.Errorf("helper update failed and previous daemon was restored without reviving interrupted shells: %w", updateErr)
}

func (r *Resolver) recheckUpdateDaemon(ctx context.Context, daemon *Daemon, expected supervisor.RuntimeMetadata) error {
	if err := r.probeDaemon(ctx, daemon); err != nil {
		return helperUnavailable("recheck daemon before helper switch", err)
	}
	metadata, known, err := r.remoteClient(daemon).RuntimeMetadata(ctx)
	if err != nil {
		return helperUnavailable("recheck running metadata before helper switch", err)
	}
	if err := verifyExpectedMetadata(metadata, known, &expected); err != nil {
		return err
	}
	return nil
}

func (r *Resolver) updateIdleAllowed(ctx context.Context, daemon *Daemon, interrupt bool) (bool, string, error) {
	err := r.remoteClient(daemon).RequireIdle(ctx)
	if err == nil {
		return true, "idle", nil
	}
	if errors.Is(err, supervisor.ErrActiveSessions) {
		if interrupt {
			return true, "active processes explicitly interrupted", nil
		}
		return false, "active PTY/exec processes; update deferred until idle", nil
	}
	if errors.Is(err, supervisor.ErrUnsupported) {
		if interrupt {
			return true, "activity unprovable; explicit interrupt accepted", nil
		}
		return false, "daemon-wide activity is unprovable; update deferred", nil
	}
	return false, "", helperUnavailable("check daemon-wide activity", err)
}

func (r *Resolver) waitDaemonStopped(ctx context.Context, daemon *Daemon) error {
	timeout := r.StartTimeout
	if timeout <= 0 {
		timeout = defaultStartTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		err := r.probeDaemon(waitCtx, daemon)
		if err == nil {
			lastErr = errors.New("daemon is still serving")
		} else {
			if daemonRefusal(err) {
				return helperUnavailable("unexpected daemon appeared during switch", err)
			}
			_, processErr := inspectHelperProcess(waitCtx, daemon.host, daemon.Target, daemon.StateDir)
			if errors.Is(processErr, errHelperProcessNotFound) {
				return nil
			}
			if processErr == nil {
				lastErr = errors.New("actual helper process is still running")
			} else {
				lastErr = processErr
			}
		}
		if waitCtx.Err() != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return fmt.Errorf("%w: old helper did not stop before switch: %v", durable.ErrUnavailable, lastErr)
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (r *Resolver) rollbackDaemon(ctx context.Context, host Host, location deploymentLocation, newBinary, oldBinary string, newMetadata, oldMetadata supervisor.RuntimeMetadata) error {
	newDaemon := newTargetDaemon(host, location, newBinary)
	process, processErr := inspectHelperProcess(ctx, host, location.target, location.paths.StateDir)
	if processErr == nil && process.binary == newBinary {
		digest, err := host.ChecksumSHA256(ctx, process.binary)
		if err != nil {
			return fmt.Errorf("digest actual executable during rollback: %w", err)
		}
		if !strings.EqualFold(digest, newMetadata.SHA256) {
			return fmt.Errorf("refusing to stop an unverified executable during rollback: %w", ErrDigestMismatch)
		}
		if err := stopHelperProcess(ctx, host, location.target, process); err != nil {
			return err
		}
		if err := r.waitDaemonStopped(ctx, newDaemon); err != nil {
			return err
		}
	} else if processErr == nil && process.binary != oldBinary {
		return fmt.Errorf("running process path changed during rollback")
	} else if processErr != nil && !errors.Is(processErr, errHelperProcessNotFound) {
		return processErr
	} else if processErr != nil {
		if err := r.probeDaemon(ctx, newDaemon); err == nil {
			return fmt.Errorf("running daemon has no provable process identity during rollback: %w", processErr)
		} else if daemonRefusal(err) {
			return fmt.Errorf("incompatible running daemon preserved during rollback: %w", err)
		}
	}
	_, err := r.connectOrStartDaemon(ctx, host, location, oldBinary, &oldMetadata)
	return err
}

func inspectHelperProcess(ctx context.Context, host Host, target Target, stateDir string) (helperProcess, error) {
	pidPath := remoteTargetJoin(target, stateDir, "helper.pid")
	var command string
	if target.GOOS == "windows" {
		script := "$ErrorActionPreference='Stop'; if (-not (Test-Path -LiteralPath " + quotePowerShell(pidPath) + ")) { exit 73 }; " +
			"$pidValue=[int](Get-Content -LiteralPath " + quotePowerShell(pidPath) + " -Raw).Trim(); " +
			"$process=Get-CimInstance Win32_Process -Filter \"ProcessId=$pidValue\"; if (-not $process) { exit 73 }; " +
			"if (-not $process.CommandLine.Contains(" + quotePowerShell(supervisor.HelperCommand) + ") -or -not $process.CommandLine.Contains(" + quotePowerShell(stateDir) + ")) { exit 74 }; " +
			"if (-not $process.ExecutablePath) { exit 74 }; [Console]::Out.WriteLine($pidValue); [Console]::Out.Write($process.ExecutablePath)"
		command = powerShellCommand("powershell.exe", script)
	} else if target.GOOS == "darwin" {
		command = "pid=$(cat " + quoteShell(pidPath) + " 2>/dev/null) || exit 73; case \"$pid\" in ''|*[!0-9]*) exit 74;; esac; " +
			"cmdline=$(ps -p \"$pid\" -o command=) || exit 73; case \"$cmdline\" in *" + supervisor.HelperCommand + "*" + quoteShell(stateDir) + "*) ;; *) exit 74;; esac; " +
			"exe=$(ps -p \"$pid\" -o comm=) || exit 74; printf '%s\\n%s\\n' \"$pid\" \"$exe\""
	} else {
		command = "pid=$(cat " + quoteShell(pidPath) + " 2>/dev/null) || exit 73; case \"$pid\" in ''|*[!0-9]*) exit 74;; esac; " +
			"[ -d /proc/\"$pid\" ] || exit 73; cmdline=$(tr '\\0' ' ' < /proc/\"$pid\"/cmdline) || exit 74; case \"$cmdline\" in *" + supervisor.HelperCommand + "*" + quoteShell(stateDir) + "*) ;; *) exit 74;; esac; " +
			"exe=$(readlink /proc/\"$pid\"/exe) || exit 74; printf '%s\\n%s\\n' \"$pid\" \"$exe\""
	}
	stdout, exitCode, err := host.Run(ctx, command, probeTimeout)
	if err != nil {
		return helperProcess{}, helperUnavailable("inspect actual helper process", err)
	}
	if exitCode == 73 {
		return helperProcess{}, fmt.Errorf("%w: %w", durable.ErrUnavailable, errHelperProcessNotFound)
	}
	if exitCode != 0 {
		return helperProcess{}, fmt.Errorf("%w: actual helper process inspection exited with %d", durable.ErrUnavailable, exitCode)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		return helperProcess{}, fmt.Errorf("%w: unexpected helper process evidence %q", durable.ErrUnavailable, stdout)
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil || pid <= 0 {
		return helperProcess{}, fmt.Errorf("%w: invalid helper pid", durable.ErrUnavailable)
	}
	binary := strings.TrimSpace(lines[1])
	if target.GOOS == "windows" && !windowsAbsolutePath(binary) || target.GOOS != "windows" && !path.IsAbs(binary) {
		return helperProcess{}, fmt.Errorf("%w: actual helper executable path is not absolute", durable.ErrUnavailable)
	}
	return helperProcess{pid: pid, binary: binary}, nil
}

func stopHelperProcess(ctx context.Context, host Host, target Target, process helperProcess) error {
	command := "kill -TERM " + strconv.Itoa(process.pid)
	if target.GOOS == "windows" {
		command = powerShellCommand("powershell.exe", "$ErrorActionPreference='Stop'; Stop-Process -Id "+strconv.Itoa(process.pid)+" -Force")
	}
	_, exitCode, err := host.Run(ctx, command, probeTimeout)
	if err != nil {
		return helperUnavailable("stop actual helper process", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("%w: stop helper process %d exited with %d", durable.ErrUnavailable, process.pid, exitCode)
	}
	return nil
}

func ensureTargetDirectories(ctx context.Context, host Host, target Target, paths TargetPaths) error {
	if err := ensureRemoteDirectory(ctx, host, target, paths.Root, true); err != nil {
		return err
	}
	return ensureRemoteDirectory(ctx, host, target, paths.BinDir, true)
}

func ensureRemoteDirectory(ctx context.Context, host Host, target Target, directory string, private bool) error {
	missing := make([]string, 0)
	current := directory
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		kind, err := remoteDirectoryKind(ctx, host, target, current)
		if err != nil {
			return err
		}
		switch kind {
		case "directory":
			if len(missing) == 0 && private {
				if err := host.Chmod(ctx, current, 0o700); err != nil {
					return helperUnavailable("mark NexTerm directory private "+current, err)
				}
			}
		case "missing":
			missing = append(missing, current)
			parent := remoteTargetDir(target, current)
			if parent == current || parent == "" {
				return fmt.Errorf("%w: no existing ancestor for %s", durable.ErrUnavailable, directory)
			}
			current = parent
			continue
		case "symlink":
			if len(missing) == 0 {
				return fmt.Errorf("%w: %s is a symbolic link", durable.ErrUnavailable, current)
			}
		default:
			return fmt.Errorf("%w: %s exists and is not a directory", durable.ErrUnavailable, current)
		}
		break
	}
	for index := len(missing) - 1; index >= 0; index-- {
		dir := missing[index]
		if err := host.Mkdir(ctx, dir); err != nil && !errors.Is(err, fs.ErrExist) {
			kind, inspectErr := remoteDirectoryKind(ctx, host, target, dir)
			if inspectErr != nil {
				return errors.Join(helperUnavailable("create remote directory "+dir, err), inspectErr)
			}
			if kind != "directory" {
				return helperUnavailable("create remote directory "+dir, err)
			}
		}
		kind, err := remoteDirectoryKind(ctx, host, target, dir)
		if err != nil {
			return err
		}
		if kind != "directory" {
			return fmt.Errorf("%w: created path %s is %s, not a private directory", durable.ErrUnavailable, dir, kind)
		}
		if private && dir == directory {
			if err := host.Chmod(ctx, dir, 0o700); err != nil {
				return helperUnavailable("mark NexTerm directory private "+dir, err)
			}
		}
	}
	return nil
}

func remoteDirectoryKind(ctx context.Context, host Host, target Target, directory string) (string, error) {
	quoted := quoteShell(directory)
	command := "# nexterm-directory-kind\nif [ -L " + quoted + " ]; then printf 'symlink\\n'; elif [ -d " + quoted + " ]; then printf 'directory\\n'; elif [ -e " + quoted + " ]; then printf 'other\\n'; else printf 'missing\\n'; fi"
	if target.GOOS == "windows" {
		script := "$ErrorActionPreference='Stop'; if (-not (Test-Path -LiteralPath " + quotePowerShell(directory) + ")) { [Console]::Out.Write('missing') } else { $item=Get-Item -LiteralPath " + quotePowerShell(directory) + " -Force; if ($item.LinkType -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { [Console]::Out.Write('symlink') } elseif ($item.PSIsContainer) { [Console]::Out.Write('directory') } else { [Console]::Out.Write('other') } }"
		command = powerShellCommand("powershell.exe", "# nexterm-directory-kind\n"+script)
	}
	stdout, exitCode, err := host.Run(ctx, command, probeTimeout)
	if err != nil {
		return "", helperUnavailable("inspect remote directory "+directory, err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("%w: inspect remote directory %s exited with %d", durable.ErrUnavailable, directory, exitCode)
	}
	kind := strings.TrimSpace(stdout)
	switch kind {
	case "directory", "missing", "symlink", "other":
		return kind, nil
	default:
		return "", fmt.Errorf("%w: unexpected directory response %q", ErrProtocol, stdout)
	}
}

var errUnsafeTemporaryRoot = errors.New("unsafe temporary helper root")

func unsafeTemporaryError(err error) error {
	if err == nil || errors.Is(err, errUnsafeTemporaryRoot) {
		return err
	}
	return fmt.Errorf("%w: %w: %w", durable.ErrUnavailable, errUnsafeTemporaryRoot, err)
}

func validateNexTermPaths(ctx context.Context, host Host, location deploymentLocation) error {
	if !location.temporary {
		return nil
	}
	for _, directory := range []string{location.paths.Root, location.paths.BinDir, location.paths.StateDir} {
		kind, err := remoteDirectoryKind(ctx, host, location.target, directory)
		if err != nil {
			return unsafeTemporaryError(err)
		}
		if kind != "missing" && kind != "directory" {
			return fmt.Errorf("%w: %w: NexTerm private path %s is %s", durable.ErrUnavailable, errUnsafeTemporaryRoot, directory, kind)
		}
	}
	return nil
}

func (r *Resolver) validateDeploymentLocation(ctx context.Context, host Host, location deploymentLocation) error {
	if !location.temporary {
		return nil
	}
	r.reportProgress(ctx, HelperProgress{Step: 3, Stage: "fallback", Path: location.paths.Root, Cause: "using a verified private temporary helper root; OS temporary-directory cleanup can remove it and will require redeployment"})
	if err := validateFallbackBase(ctx, host, location); err != nil {
		return unsafeTemporaryError(err)
	}
	return validateNexTermPaths(ctx, host, location)
}

func validateFallbackBase(ctx context.Context, host Host, location deploymentLocation) error {
	if !location.temporary || location.base == "" {
		return fmt.Errorf("%w: temporary helper base is not configured", durable.ErrUnavailable)
	}
	if strings.HasPrefix(location.base, "/tmp/nexterm-") {
		if err := ensureRemoteDirectory(ctx, host, location.target, location.base, true); err != nil {
			return err
		}
	}
	var command string
	if location.target.GOOS == "windows" {
		script := "$ErrorActionPreference='Stop'; $item=Get-Item -LiteralPath " + quotePowerShell(location.base) + " -Force; if (-not $item.PSIsContainer -or $item.LinkType -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { exit 74 }; " +
			"$owner=(Get-Acl -LiteralPath " + quotePowerShell(location.base) + ").Owner; $identity=[Security.Principal.WindowsIdentity]::GetCurrent(); " +
			"$sid=$identity.User.Translate([Security.Principal.NTAccount]).Value; if ($owner -ne $identity.Name -and $owner -ne $sid) { exit 74 }; [Console]::Out.Write('ok')"
		command = powerShellCommand("powershell.exe", script)
	} else {
		quoted := quoteShell(location.base)
		stat := "stat -c '%u %a' -- "
		if location.target.GOOS == "darwin" {
			stat = "stat -f '%u %Lp' -- "
		}
		command = "# nexterm-private-base\nif [ -L " + quoted + " ] || [ ! -d " + quoted + " ]; then exit 74; fi; " +
			"set -- $(" + stat + quoted + ") || exit 74; [ \"$1\" = \"$(id -u)\" ] && [ \"$2\" = 700 ] || exit 74; printf 'ok\\n'"
	}
	stdout, exitCode, err := host.Run(ctx, command, probeTimeout)
	if err != nil {
		return helperUnavailable("validate private temporary helper directory", err)
	}
	if exitCode != 0 || strings.TrimSpace(stdout) != "ok" {
		return fmt.Errorf("%w: %w: temporary helper base %s must be a non-symlink directory owned by the target user with private permissions", durable.ErrUnavailable, errUnsafeTemporaryRoot, location.base)
	}
	return nil
}

func deploymentPathUnavailable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrTrust) || errors.Is(err, ErrDigestMismatch) || errors.Is(err, ErrProtocol) || errors.Is(err, supervisor.ErrProtocol) {
		return false
	}
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrNoExec) || errors.Is(err, errUnsafeTemporaryRoot) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{"permission denied", "access denied", "operation not permitted", "read-only file system", "noexec", "not a directory"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func nonEmptyLines(output string) []string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func (h *clientHost) Delete(ctx context.Context, path string, directory bool) error {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return err
	}
	return filesystem.Delete(ctx, path, directory)
}
