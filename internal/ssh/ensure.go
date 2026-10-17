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
	for _, binary := range lookupPathBinaries(ctx, host) {
		if !compatible(ctx, host, binary) {
			continue
		}
		if daemon, err := r.startOrConnect(ctx, host, binary, stateDir, digest); err == nil {
			return daemon, nil
		} else if daemonRefusal(err) {
			return nil, err
		}
	}
	upload, standalone, sourceErr := r.uploadSource(info)
	if sourceErr != nil && !r.DisableAutoInstall {
		return nil, sourceErr
	}
	if upload == nil {
		if r.DisableAutoInstall {
			return nil, fmt.Errorf("%w: no compatible helper is installed and automatic helper installation is disabled", durable.ErrUnavailable)
		}
		if sourceErr != nil {
			return nil, sourceErr
		}
		return nil, fmt.Errorf("%w: no supervisor-capable binary on the target and no helper source is configured", durable.ErrUnavailable)
	}
	validationErr := validateUpload(upload, info)
	if validationErr != nil && !r.DisableAutoInstall {
		return nil, validationErr
	}
	if validationErr == nil {
		cached := uploadPath(binDir, upload, standalone)
		if exists, err := host.Exists(ctx, cached); err != nil {
			if !r.DisableAutoInstall {
				return nil, fmt.Errorf("%w: check cached helper %s: %v", durable.ErrUnavailable, cached, err)
			}
		} else if exists {
			if err := verifyRemoteBinary(ctx, host, cached, upload.Digest); err != nil {
				if standalone && !r.DisableAutoInstall {
					return nil, err
				}
			} else if daemon, err := r.startOrConnect(ctx, host, cached, stateDir, digest); err == nil {
				return daemon, nil
			} else if daemonRefusal(err) {
				return nil, err
			}
		}
	}
	if r.DisableAutoInstall {
		return nil, fmt.Errorf("%w: no compatible helper is installed and automatic helper installation is disabled", durable.ErrUnavailable)
	}
	binary, err := r.install(ctx, host, binDir, upload, standalone)
	if err != nil {
		return nil, err
	}
	return r.startOrConnect(ctx, host, binary, stateDir, digest)
}

func (r *Resolver) uploadSource(info targetInfo) (*UploadSource, bool, error) {
	inspect := r.inspect
	if inspect == nil {
		inspect = InspectExecutable
	}
	if r.HelperExecutable != "" {
		source, err := inspect(r.HelperExecutable)
		if err != nil {
			return nil, true, fmt.Errorf("inspect offline helper %s: %w", r.HelperExecutable, err)
		}
		return &source, true, nil
	}
	if r.Executable == "" {
		return nil, false, nil
	}
	name := strings.ToLower(filepath.Base(r.Executable))
	if strings.HasPrefix(name, "nexterm") {
		candidate := filepath.Join(filepath.Dir(r.Executable), "nexterm-helper-"+info.goos+"-"+info.goarch)
		source, err := inspect(candidate)
		if err == nil && filepath.Clean(source.Path) == filepath.Clean(candidate) {
			return &source, true, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, true, fmt.Errorf("inspect target helper %s: %w", candidate, err)
		}
	}
	source, err := inspect(r.Executable)
	if err != nil {
		return nil, false, err
	}
	return &source, false, nil
}

func uploadPath(binDir string, upload *UploadSource, standalone bool) string {
	if standalone {
		return binDir + "/nexterm-helper-" + upload.Digest
	}
	return binDir + "/nexterm-" + upload.Digest[:12]
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

func lookupPathBinaries(ctx context.Context, host Host) []string {
	stdout, _, err := host.Run(ctx, "command -v nexterm-helper nexterm-server nexterm-desktop nexterm 2>/dev/null; true", probeTimeout)
	if err != nil {
		return nil
	}
	return nonEmptyLines(stdout)
}

func compatible(ctx context.Context, host Host, binary string) bool {
	return probeBinary(ctx, host, binary) == nil
}

func verifyRemoteBinary(ctx context.Context, host Host, binary, expectedDigest string) error {
	actualDigest, err := host.ChecksumSHA256(ctx, binary)
	if err != nil {
		return fmt.Errorf("%w: digest cached helper %s: %v", durable.ErrUnavailable, binary, err)
	}
	if !strings.EqualFold(actualDigest, expectedDigest) {
		return fmt.Errorf("%w: cached helper digest mismatch for %s: expected %s, got %s", durable.ErrUnavailable, binary, expectedDigest, actualDigest)
	}
	return probeBinary(ctx, host, binary)
}

func probeBinary(ctx context.Context, host Host, binary string) error {
	stdout, exitCode, err := host.Run(ctx, quoteShell(binary)+" "+supervisor.HelperCommand+" --probe", probeTimeout)
	if err != nil {
		return fmt.Errorf("%w: execute helper probe %s: %v", durable.ErrUnavailable, binary, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("%w: helper probe %s exited with %d; check user-directory execute permission and noexec mount policy", durable.ErrUnavailable, binary, exitCode)
	}
	if actual := strings.TrimSpace(stdout); actual != strconv.Itoa(supervisor.ProtocolVersion) {
		return fmt.Errorf("%w: helper probe %s reported protocol %q, want %d", durable.ErrUnavailable, binary, actual, supervisor.ProtocolVersion)
	}
	return nil
}

func (r *Resolver) startOrConnect(ctx context.Context, host Host, binary, stateDir, digest string) (*Daemon, error) {
	daemon := &Daemon{host: host, Binary: binary, StateDir: stateDir, Digest: digest}
	if err := probeDaemon(ctx, daemon, probeTimeout); err == nil {
		return daemon, nil
	} else if errors.Is(err, supervisor.ErrVersionMismatch) {
		return nil, fmt.Errorf("%w: the running supervisor daemon speaks an incompatible protocol; refusing to replace it: %w", durable.ErrUnavailable, err)
	} else if errors.Is(err, supervisor.ErrStateMismatch) {
		return nil, fmt.Errorf("%w: the running supervisor daemon owns a different state directory; refusing to replace it: %w", durable.ErrUnavailable, err)
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
		} else if daemonRefusal(err) {
			return nil, fmt.Errorf("%w: the spawned supervisor daemon cannot serve this client; refusing to replace the running one: %w", durable.ErrUnavailable, err)
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

func daemonRefusal(err error) bool {
	return errors.Is(err, supervisor.ErrVersionMismatch) || errors.Is(err, supervisor.ErrStateMismatch)
}

func probeDaemon(ctx context.Context, daemon *Daemon, timeout time.Duration) error {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := supervisor.NewRemoteClient(daemon.dial, daemon.Digest).List(probeCtx)
	return err
}

func spawnDaemon(ctx context.Context, host Host, binary, stateDir string) error {
	command := "mkdir -p -- " + quoteShell(stateDir) + " && chmod 700 " + quoteShell(stateDir) +
		" && { setsid " + quoteShell(binary) + " " + supervisor.HelperCommand + " --state-dir " + quoteShell(stateDir) +
		" >>" + quoteShell(stateDir+"/helper.log") + " 2>&1 </dev/null & }"
	if _, exitCode, err := host.Run(ctx, command, probeTimeout); err != nil {
		return fmt.Errorf("%w: spawn supervisor daemon: %v", durable.ErrUnavailable, err)
	} else if exitCode != 0 {
		return fmt.Errorf("%w: prepare supervisor daemon exited with %d", durable.ErrUnavailable, exitCode)
	}
	return nil
}

func validateUpload(upload *UploadSource, info targetInfo) error {
	if len(upload.Digest) != 64 {
		return fmt.Errorf("%w: the local binary digest is malformed", durable.ErrUnavailable)
	}
	if _, err := hex.DecodeString(upload.Digest); err != nil {
		return fmt.Errorf("%w: the local binary digest is malformed: %v", durable.ErrUnavailable, err)
	}
	if upload.GOOS != info.goos || upload.GOARCH != info.goarch {
		return fmt.Errorf("%w: the local binary targets %s/%s but the remote host is %s/%s; install a matching nexterm binary on the target instead", base.ErrUnsupported, upload.GOOS, upload.GOARCH, info.goos, info.goarch)
	}
	if upload.CGO {
		return fmt.Errorf("%w: the local binary links CGO and may not run on the target; install a matching nexterm binary on the target instead", base.ErrUnsupported)
	}
	return nil
}

func (r *Resolver) install(ctx context.Context, host Host, binDir string, upload *UploadSource, standalone bool) (target string, retErr error) {
	uploadCtx, cancel := context.WithTimeout(ctx, r.uploadTimeout())
	defer cancel()
	for _, dir := range []string{path.Dir(binDir), binDir} {
		if err := ensureRemoteDir(uploadCtx, host, dir); err != nil {
			return "", err
		}
	}
	target = uploadPath(binDir, upload, standalone)
	lock := binDir + "/.nexterm-" + upload.Digest + ".install"
	for {
		ready, err := cachedBinary(uploadCtx, host, target, upload.Digest)
		if err != nil {
			if standalone {
				return "", err
			}
		} else if ready {
			return target, nil
		}
		err = host.Mkdir(uploadCtx, lock)
		if err == nil {
			break
		}
		exists, existsErr := host.Exists(uploadCtx, lock)
		if existsErr != nil {
			return "", fmt.Errorf("%w: inspect helper installation lock %s: %v", durable.ErrUnavailable, lock, existsErr)
		}
		if !exists {
			return "", fmt.Errorf("%w: create helper installation lock %s: %v", durable.ErrUnavailable, lock, err)
		}
		select {
		case <-uploadCtx.Done():
			return "", fmt.Errorf("%w: wait for concurrent helper installation: %w", durable.ErrUnavailable, context.Cause(uploadCtx))
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		if err := host.Delete(cleanupCtx, lock, true); err != nil && !errors.Is(err, fs.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("%w: remove helper installation lock %s: %v", durable.ErrUnavailable, lock, err))
		}
	}()
	ready, err := cachedBinary(uploadCtx, host, target, upload.Digest)
	if err != nil {
		if standalone {
			return "", err
		}
	} else if ready {
		return target, nil
	}
	return uploadStaged(uploadCtx, host, binDir, target, upload, standalone)
}

func (r *Resolver) uploadTimeout() time.Duration {
	if r.UploadTimeout > 0 {
		return r.UploadTimeout
	}
	return defaultUploadTimeout
}

func cachedBinary(ctx context.Context, host Host, target, digest string) (bool, error) {
	exists, err := host.Exists(ctx, target)
	if err != nil {
		return false, fmt.Errorf("%w: check cached helper %s: %v", durable.ErrUnavailable, target, err)
	}
	if !exists {
		return false, nil
	}
	if err := verifyRemoteBinary(ctx, host, target, digest); err != nil {
		return true, err
	}
	return true, nil
}

func uploadStaged(ctx context.Context, host Host, binDir, target string, upload *UploadSource, probe bool) (result string, retErr error) {
	name := path.Base(target)
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("%w: stage helper binary: %v", durable.ErrUnavailable, err)
	}
	staging := binDir + "/." + name + "." + hex.EncodeToString(nonce[:]) + ".partial"
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		if err := host.Delete(cleanupCtx, staging, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("%w: remove partial helper upload %s: %v", durable.ErrUnavailable, staging, err))
		}
	}()
	if _, err := host.Upload(ctx, upload.Path, staging); err != nil {
		return "", fmt.Errorf("%w: upload helper binary: %w", durable.ErrUnavailable, err)
	}
	if err := host.Chmod(ctx, staging, 0o700); err != nil {
		return "", fmt.Errorf("%w: mark uploaded helper executable: %v", durable.ErrUnavailable, err)
	}
	digest, err := host.ChecksumSHA256(ctx, staging)
	if err != nil {
		return "", fmt.Errorf("%w: digest staged helper %s: %v", durable.ErrUnavailable, staging, err)
	}
	if !strings.EqualFold(digest, upload.Digest) {
		return "", fmt.Errorf("%w: uploaded helper digest mismatch: expected %s, got %s", durable.ErrUnavailable, upload.Digest, digest)
	}
	if probe {
		if err := probeBinary(ctx, host, staging); err != nil {
			return "", err
		}
	}
	if err := host.Rename(ctx, staging, target); err != nil {
		return "", fmt.Errorf("%w: commit uploaded helper: %v", durable.ErrUnavailable, err)
	}
	committed = true
	digest, err = host.ChecksumSHA256(ctx, target)
	if err != nil {
		return "", fmt.Errorf("%w: digest installed helper %s: %v", durable.ErrUnavailable, target, err)
	}
	if !strings.EqualFold(digest, upload.Digest) {
		return "", fmt.Errorf("%w: installed helper digest mismatch: expected %s, got %s", durable.ErrUnavailable, upload.Digest, digest)
	}
	return target, nil
}

func (h *clientHost) Delete(ctx context.Context, path string, directory bool) error {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return err
	}
	return filesystem.Delete(ctx, path, directory)
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
