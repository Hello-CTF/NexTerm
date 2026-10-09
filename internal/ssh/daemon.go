// Package ssh starts or connects to the NexTerm supervisor daemon on an SSH
// target through the session's existing SSH channel. It uploads the local
// supervisor-capable binary only when the target has no compatible one, and
// speaks the supervisor protocol over exec-channel bridges, so no tmux or
// extra listener is required on the target.
package ssh

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

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

// Daemon is a connected handle to a supervisor daemon on an SSH target.
type Daemon struct {
	host     Host
	Binary   string
	StateDir string
	Digest   string
}

func (d *Daemon) dial(ctx context.Context) (net.Conn, error) {
	return d.host.DialExec(ctx, "exec "+quoteShell(d.Binary)+" "+supervisor.HelperCommand+" --bridge --state-dir "+quoteShell(d.StateDir))
}

// Provider returns the durable provider speaking to this daemon.
func (d *Daemon) Provider() *supervisor.RemoteProvider {
	return supervisor.NewRemoteProvider(supervisor.NewRemoteClient(d.dial, d.Digest))
}

// Resolver resolves the durable provider for an SSH session transport,
// starting or connecting to the target's supervisor daemon on demand.
type Resolver struct {
	// Executable is the local supervisor-capable binary uploaded to targets
	// without a compatible one. Empty disables uploads.
	Executable string
	// StartTimeout bounds daemon spawn plus readiness probing.
	StartTimeout time.Duration
	// UploadTimeout bounds the SFTP upload of Executable.
	UploadTimeout time.Duration

	hostFor func(ctx context.Context, client *transportssh.Client) (Host, error)
	inspect func(path string) (UploadSource, error)
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
// an existing compatible binary when needed and uploading Executable as a
// last resort.
func (r *Resolver) EnsureDaemon(ctx context.Context, client *transportssh.Client) (*Daemon, error) {
	host, err := r.host(ctx, client)
	if err != nil {
		return nil, err
	}
	return r.ensureOnHost(ctx, host)
}

const (
	defaultStartTimeout  = 30 * time.Second
	defaultUploadTimeout = 10 * time.Minute
	probeTimeout         = 10 * time.Second
)
