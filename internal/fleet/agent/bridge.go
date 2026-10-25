package agent

import (
	"context"
	"io"
	"os/exec"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/coder/websocket"
)

// BridgeSpawner opens a local byte stream carrying the supervisor protocol
// for one state directory.
type BridgeSpawner func(ctx context.Context, stateDir string) (io.ReadWriteCloser, error)

// DirectHelperBridge uses the helper's local socket or Windows named pipe.
func DirectHelperBridge(ctx context.Context, stateDir string) (io.ReadWriteCloser, error) {
	return supervisor.DialHelperBridge(ctx, stateDir)
}

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
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = coalescePump(pipe, bridgeReadSize, bridgeMergeLimit, func(chunk []byte) error {
			writeCtx, cancel := context.WithTimeout(ctx, defaultWSPingTimeout)
			defer cancel()
			return conn.Write(writeCtx, websocket.MessageBinary, chunk)
		})
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
	_ = pipe.Close()
	_ = conn.Close(websocket.StatusNormalClosure, "bridge closed")
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

const (
	bridgeReadSize   = 64 * 1024
	bridgeMergeLimit = 32 * 1024
	bridgeReadAhead  = 4
)

type pumpChunk struct {
	buffer []byte
	count  int
	err    error
}

// coalescePump copies src to send. A reader goroutine keeps reading while a send is in flight, so when the
// network is slower than the local pipe the chunks that piled up meanwhile are merged (up to mergeLimit bytes) into
// one message. Nothing waits for more data: only chunks that are already read are merged, so latency is unchanged.
// Chunks of mergeLimit bytes or more are sent as read, without copying.
func coalescePump(src io.Reader, readSize, mergeLimit int, send func([]byte) error) error {
	free := make(chan []byte, bridgeReadAhead)
	for i := 0; i < bridgeReadAhead; i++ {
		free <- make([]byte, readSize)
	}
	filled := make(chan pumpChunk, bridgeReadAhead)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer close(filled)
		for {
			var buffer []byte
			select {
			case buffer = <-free:
			case <-stop:
				return
			}
			count, err := src.Read(buffer)
			select {
			case filled <- pumpChunk{buffer: buffer, count: count, err: err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	merged := make([]byte, 0, mergeLimit)
	for chunk := range filled {
		if chunk.count > 0 && chunk.count >= mergeLimit {
			err := send(chunk.buffer[:chunk.count])
			free <- chunk.buffer
			if err != nil {
				return err
			}
		} else if chunk.count > 0 {
			merged = append(merged[:0], chunk.buffer[:chunk.count]...)
			free <- chunk.buffer
			var carry *pumpChunk
		drain:
			for len(merged) < mergeLimit {
				select {
				case next, ok := <-filled:
					if !ok {
						break drain
					}
					if next.count > 0 && len(merged)+next.count > mergeLimit {
						carry = &next
						break drain
					}
					merged = append(merged, next.buffer[:next.count]...)
					free <- next.buffer
					if next.err != nil {
						carry = &pumpChunk{err: next.err}
						break drain
					}
				default:
					break drain
				}
			}
			if err := send(merged); err != nil {
				return err
			}
			if carry != nil {
				if carry.err != nil {
					return nil
				}
				if err := send(carry.buffer[:carry.count]); err != nil {
					return err
				}
				free <- carry.buffer
			}
		}
		if chunk.err != nil {
			return nil
		}
	}
	return nil
}
