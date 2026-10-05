package ssh

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type targetInfo struct {
	dataRoot string
	goos     string
	goarch   string
	euid     string
}

func (r *Resolver) ensureOnHost(ctx context.Context, host Host) (*Daemon, error) {
	info, err := discoverTarget(ctx, host)
	if err != nil {
		return nil, err
	}
	root := info.dataRoot + "/nexterm"
	binDir := root + "/bin"
	stateDir := root + "/daemon"
	digest, err := supervisor.StateDigest(stateDir, info.euid)
	if err != nil {
		return nil, err
	}
	var upload *UploadSource
	if r.Executable != "" {
		inspect := r.inspect
		if inspect == nil {
			inspect = InspectExecutable
		}
		source, err := inspect(r.Executable)
		if err != nil {
			return nil, err
		}
		upload = &source
		if len(upload.Digest) >= 12 {
			cached := binDir + "/nexterm-" + upload.Digest[:12]
			if exists, err := host.Exists(ctx, cached); err == nil && exists && compatible(ctx, host, cached) {
				if daemon, err := r.startOrConnect(ctx, host, cached, stateDir, digest); err == nil {
					return daemon, nil
				}
			}
		}
	}
	if binary := lookupPathBinary(ctx, host); binary != "" && compatible(ctx, host, binary) {
		if daemon, err := r.startOrConnect(ctx, host, binary, stateDir, digest); err == nil {
			return daemon, nil
		}
	}
	if upload == nil {
		return nil, fmt.Errorf("%w: no supervisor-capable binary on the target and uploads are disabled", durable.ErrUnavailable)
	}
	if err := validateUpload(upload, info); err != nil {
		return nil, err
	}
	binary, err := r.upload(ctx, host, binDir, upload)
	if err != nil {
		return nil, err
	}
	return r.startOrConnect(ctx, host, binary, stateDir, digest)
}

func discoverTarget(ctx context.Context, host Host) (targetInfo, error) {
	const script = `printf '%s\n' "${XDG_DATA_HOME:-$HOME/.local/share}"; uname -s; uname -m; id -u`
	stdout, exitCode, err := host.Run(ctx, script, probeTimeout)
	if err != nil {
		return targetInfo{}, fmt.Errorf("%w: target discovery: %v", durable.ErrUnavailable, err)
	}
	if exitCode != 0 {
		return targetInfo{}, fmt.Errorf("%w: target discovery exited with %d", durable.ErrUnavailable, exitCode)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 4 {
		return targetInfo{}, fmt.Errorf("%w: unexpected target discovery output %q", durable.ErrUnavailable, stdout)
	}
	info := targetInfo{dataRoot: lines[0], goos: strings.ToLower(lines[1]), goarch: unameToGOARCH(lines[2]), euid: lines[3]}
	if info.goos != "linux" {
		return targetInfo{}, fmt.Errorf("%w: the supervisor daemon supports linux targets, target reports %s", base.ErrUnsupported, lines[1])
	}
	if info.goarch == "" {
		return targetInfo{}, fmt.Errorf("%w: unsupported target architecture %q", base.ErrUnsupported, lines[2])
	}
	if _, err := strconv.Atoi(info.euid); err != nil {
		return targetInfo{}, fmt.Errorf("%w: target discovery returned non-numeric uid %q", durable.ErrUnavailable, info.euid)
	}
	return info, nil
}

func unameToGOARCH(uname string) string {
	switch uname {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return ""
	}
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

func lookupPathBinary(ctx context.Context, host Host) string {
	stdout, _, err := host.Run(ctx, "command -v nexterm-server nexterm-desktop nexterm 2>/dev/null; true", probeTimeout)
	if err != nil {
		return ""
	}
	lines := nonEmptyLines(stdout)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func compatible(ctx context.Context, host Host, binary string) bool {
	stdout, exitCode, err := host.Run(ctx, quoteShell(binary)+" "+supervisor.HelperCommand+" --probe", probeTimeout)
	if err != nil || exitCode != 0 {
		return false
	}
	return strings.TrimSpace(stdout) == strconv.Itoa(supervisor.ProtocolVersion)
}

func (r *Resolver) startOrConnect(ctx context.Context, host Host, binary, stateDir, digest string) (*Daemon, error) {
	daemon := &Daemon{host: host, Binary: binary, StateDir: stateDir, Digest: digest}
	if err := probeDaemon(ctx, daemon, probeTimeout); err == nil {
		return daemon, nil
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
		if err := probeDaemon(ctx, daemon, probeTimeout); err == nil {
			return daemon, nil
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

func probeDaemon(ctx context.Context, daemon *Daemon, timeout time.Duration) error {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := daemon.Provider().ListDurable(probeCtx)
	return err
}

func spawnDaemon(ctx context.Context, host Host, binary, stateDir string) error {
	command := "mkdir -p -- " + quoteShell(stateDir) + " && chmod 700 " + quoteShell(stateDir) +
		" && setsid " + quoteShell(binary) + " " + supervisor.HelperCommand + " --state-dir " + quoteShell(stateDir) +
		" >>" + quoteShell(stateDir+"/helper.log") + " 2>&1 </dev/null &"
	if _, _, err := host.Run(ctx, command, probeTimeout); err != nil {
		return fmt.Errorf("%w: spawn supervisor daemon: %v", durable.ErrUnavailable, err)
	}
	return nil
}

func validateUpload(upload *UploadSource, info targetInfo) error {
	if len(upload.Digest) != 64 {
		return fmt.Errorf("%w: the local binary digest is malformed", durable.ErrUnavailable)
	}
	if upload.GOOS != info.goos || upload.GOARCH != info.goarch {
		return fmt.Errorf("%w: the local binary targets %s/%s but the remote host is %s/%s; install a matching nexterm binary on the target instead", base.ErrUnsupported, upload.GOOS, upload.GOARCH, info.goos, info.goarch)
	}
	if upload.CGO {
		return fmt.Errorf("%w: the local binary links CGO and may not run on the target; install a matching nexterm binary on the target instead", base.ErrUnsupported)
	}
	return nil
}

func (r *Resolver) upload(ctx context.Context, host Host, binDir string, upload *UploadSource) (string, error) {
	timeout := r.UploadTimeout
	if timeout <= 0 {
		timeout = defaultUploadTimeout
	}
	uploadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, dir := range []string{path.Dir(binDir), binDir} {
		if err := ensureRemoteDir(uploadCtx, host, dir); err != nil {
			return "", err
		}
	}
	name := "nexterm-" + upload.Digest[:12]
	target := binDir + "/" + name
	staging := binDir + "/." + name + ".partial"
	if _, err := host.Upload(uploadCtx, upload.Path, staging); err != nil {
		return "", fmt.Errorf("%w: upload supervisor binary: %v", durable.ErrUnavailable, err)
	}
	if err := host.Chmod(uploadCtx, staging, 0o700); err != nil {
		return "", fmt.Errorf("%w: mark uploaded binary executable: %v", durable.ErrUnavailable, err)
	}
	if err := host.Rename(uploadCtx, staging, target); err != nil {
		return "", fmt.Errorf("%w: commit uploaded binary: %v", durable.ErrUnavailable, err)
	}
	digest, err := host.ChecksumSHA256(uploadCtx, target)
	if err != nil {
		return "", fmt.Errorf("%w: digest uploaded binary: %v", durable.ErrUnavailable, err)
	}
	if digest != upload.Digest {
		return "", fmt.Errorf("%w: uploaded binary digest mismatch", durable.ErrUnavailable)
	}
	return target, nil
}

func ensureRemoteDir(ctx context.Context, host Host, dir string) error {
	if err := host.Mkdir(ctx, dir); err != nil {
		if exists, existsErr := host.Exists(ctx, dir); existsErr != nil || !exists {
			return fmt.Errorf("%w: create remote directory: %v", durable.ErrUnavailable, err)
		}
	}
	if err := host.Chmod(ctx, dir, 0o700); err != nil {
		return fmt.Errorf("%w: mark remote directory private: %v", durable.ErrUnavailable, err)
	}
	return nil
}
