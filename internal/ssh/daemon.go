// Package ssh starts or connects to the NexTerm supervisor daemon on an SSH
// target through the session's existing SSH channel. It uploads a standalone
// target helper only when no compatible daemon can be used, and speaks the
// supervisor protocol over exec-channel bridges, so no tmux or extra listener
// is required on the target.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

var ErrHelperNotFound = errors.New("standalone target helper not found")

// Runner executes a command on the target and reports its outcome. err is
// reserved for transport failures; a nonzero exit is reported via exitCode.
type Runner interface {
	Run(ctx context.Context, command string, timeout time.Duration) (stdout string, exitCode int, err error)
}

// FileSystem is the remote filesystem surface needed to stage an upload.
type FileSystem interface {
	Exists(ctx context.Context, path string) (bool, error)
	Mkdir(ctx context.Context, path string) error
	Chmod(ctx context.Context, path string, mode fs.FileMode) error
	Upload(ctx context.Context, localPath, remotePath string) (int64, error)
	Rename(ctx context.Context, from, to string) error
	Delete(ctx context.Context, path string, directory bool) error
	ChecksumSHA256(ctx context.Context, path string) (string, error)
}

// Dialer opens a net.Conn carrying an exec channel running command.
type Dialer interface {
	DialExec(ctx context.Context, command string) (net.Conn, error)
}

// Host bundles the SSH channel surfaces the daemon lifecycle needs.
type Host interface {
	Runner
	FileSystem
	Dialer
}

// HelperUpdateStatus describes an update attempted while ensuring a daemon.
// RolledBack never implies that interrupted shells are alive again.
type HelperUpdateStatus struct {
	Deferred    bool
	Updated     bool
	Interrupted bool
	RolledBack  bool
	Reason      string
	Local       supervisor.RuntimeMetadata
	Running     supervisor.RuntimeMetadata
}

// Daemon is a connected handle to a supervisor daemon on an SSH target.
type Daemon struct {
	host     Host
	Binary   string
	StateDir string
	Digest   string
	Target   Target

	RunningMetadata supervisor.RuntimeMetadata
	MetadataKnown   bool
	Temporary       bool
	Update          HelperUpdateStatus
}

func (d *Daemon) dial(ctx context.Context) (net.Conn, error) {
	command := "exec " + quoteShell(d.Binary) + " " + supervisor.HelperCommand + " --bridge --state-dir " + quoteShell(d.StateDir)
	if d.Target.GOOS == "windows" {
		command = powerShellCommand("powershell.exe", "& "+quotePowerShell(d.Binary)+" "+supervisor.HelperCommand+" --bridge --state-dir "+quotePowerShell(d.StateDir))
	}
	return d.host.DialExec(ctx, command)
}

// Client returns a new protocol client for this daemon. Each metadata or
// activity call performs a fresh handshake through this client.
func (d *Daemon) Client() *supervisor.Client {
	return supervisor.NewRemoteClient(d.dial, d.Digest)
}

// Provider returns the durable provider speaking to this daemon.
func (d *Daemon) Provider() *supervisor.RemoteProvider {
	return supervisor.NewRemoteProvider(d.Client())
}

func (d *Daemon) RuntimeMetadata(ctx context.Context) (supervisor.RuntimeMetadata, bool, error) {
	return d.Client().RuntimeMetadata(ctx)
}

func (d *Daemon) RequireIdle(ctx context.Context) error {
	return d.Client().RequireIdle(ctx)
}

// EnsureOptions controls potentially disruptive update behavior. Compatible
// release differences are accepted with exact protocol and state checks;
// active processes are interrupted only when InterruptActive is explicitly set.
type EnsureOptions struct {
	InterruptActive bool
}

// Resolver resolves the durable provider for an SSH session transport,
// starting or connecting to the target's supervisor daemon on demand.
type Resolver struct {
	// Executable locates the required standalone target helper beside the app.
	Executable string
	// HelperExecutable explicitly selects an offline standalone helper import.
	HelperExecutable string
	// DisableAutoInstall keeps installed and running helpers usable but forbids uploads.
	DisableAutoInstall bool
	// StartTimeout bounds each daemon spawn plus readiness sequence, within EnsureTimeout.
	StartTimeout time.Duration
	// UploadTimeout bounds each staging sequence, within EnsureTimeout.
	UploadTimeout time.Duration
	// EnsureTimeout is the shared budget for discovery, both upload transports,
	// backoff and startup across all three complete cycles. It defaults to ten minutes.
	EnsureTimeout time.Duration
	// RetryBackoff is the initial delay between complete cycles. It is not an
	// additional attempt and remains inside EnsureTimeout.
	RetryBackoff time.Duration
	// Progress receives helper installation and update progress. It is optional.
	Progress func(context.Context, HelperProgress)

	hostFor   func(ctx context.Context, client *transportssh.Client) (Host, error)
	inspect   func(path string) (UploadSource, error)
	clientFor func(daemon *Daemon) remoteDaemonClient
}

type HelperProgress struct {
	OperationID string
	Attempt     int
	Attempts    int
	Deadline    time.Time
	Step        int
	Steps       int
	Stage       string
	Cause       string
	Transport   string
	Path        string
	Transferred int64
	Total       int64
	Percent     int
	Done        bool
	Error       string
}

func (r *Resolver) ResolveDurable(ctx context.Context, transport base.Transport) (base.DurableProvider, error) {
	client, ok := transport.(*transportssh.Client)
	if !ok {
		return nil, fmt.Errorf("%w: remote durable tabs need an SSH transport", base.ErrUnsupported)
	}
	daemon, err := r.EnsureDaemon(ctx, client)
	if err != nil {
		return nil, err
	}
	return daemon.Provider(), nil
}

// EnsureDaemon connects to the target's supervisor daemon, starting it with
// an existing compatible standalone helper or uploading one as a last resort.
func (r *Resolver) EnsureDaemon(ctx context.Context, client *transportssh.Client) (*Daemon, error) {
	return r.EnsureDaemonWithOptions(ctx, client, EnsureOptions{})
}

func (r *Resolver) EnsureDaemonWithOptions(ctx context.Context, client *transportssh.Client, options EnsureOptions) (*Daemon, error) {
	host, err := r.host(ctx, client)
	if err != nil {
		return nil, err
	}
	return r.ensureOnHostWithClient(ctx, host, client, options)
}

const (
	defaultEnsureTimeout = 10 * time.Minute
	defaultStartTimeout  = 30 * time.Second
	defaultUploadTimeout = 10 * time.Minute
	defaultRetryBackoff  = 250 * time.Millisecond
	maxEnsureAttempts    = 3
	probeTimeout         = 10 * time.Second
)
