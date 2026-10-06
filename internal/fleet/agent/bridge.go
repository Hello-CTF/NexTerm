package agent

import (
	"context"
	"io"
	"os/exec"

	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
	"github.com/coder/websocket"
)

// BridgeSpawner starts a local supervisor helper bridge whose stdin/stdout
// carry the supervisor protocol for one state directory.
type BridgeSpawner func(ctx context.Context, stateDir string) (io.ReadWriteCloser, error)

// SubprocessBridge spawns the same binary's supervisor-helper in bridge mode,
// mirroring how the SSH transport reaches remote helpers; no inbound listener
// is involved.
func SubprocessBridge(executable string) BridgeSpawner {
	return func(ctx context.Context, stateDir string) (io.ReadWriteCloser, error) {
		command := exec.CommandContext(ctx, executable, supervisor.HelperCommand, "--bridge", "--state-dir", stateDir)
		stdin, err := command.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := command.Start(); err != nil {
			return nil, err
		}
		return &subprocessPipe{stdin: stdin, stdout: stdout, command: command}, nil
	}
}

type subprocessPipe struct {
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	command *exec.Cmd
}

func (p *subprocessPipe) Read(buffer []byte) (int, error)  { return p.stdout.Read(buffer) }
func (p *subprocessPipe) Write(buffer []byte) (int, error) { return p.stdin.Write(buffer) }

func (p *subprocessPipe) Close() error {
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	if p.command.Process != nil {
		_ = p.command.Process.Kill()
		_, _ = p.command.Process.Wait()
	}
	return nil
}

// BridgePipe shuttles raw supervisor protocol bytes between the bridge
// connection and the local helper bridge until either side fails or the
// context ends. It is byte-transparent, so protocol-level resume (attach
// with an expected incarnation, version checks) passes through unchanged.
func BridgePipe(ctx context.Context, conn *websocket.Conn, spawn BridgeSpawner, stateDir string) error {
	pipe, err := spawn(ctx, stateDir)
	if err != nil {
		return err
	}
	defer func() { _ = pipe.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 32*1024)
		for {
			count, err := pipe.Read(buffer)
			if count > 0 {
				writeCtx, cancel := context.WithTimeout(ctx, defaultWSPingTimeout)
				err = conn.Write(writeCtx, websocket.MessageBinary, buffer[:count])
				cancel()
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if _, err := pipe.Write(payload); err != nil {
			break
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "bridge closed")
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}
