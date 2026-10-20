package deviceproto

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// A bare conn end is never a clean operation result: every operation
// concludes with an explicit frame (FrameEnd, FrameDialEOF, FrameOK, or
// FrameError), so EOF before that frame is a truncated operation.

func TestClientReadStreamUnexpectedEOFBeforeEnd(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameOpenRead {
			return
		}
		if err := WriteMessage(conn, FrameOpened, OpenedMsg{Size: 100}); err != nil {
			return
		}
		if err := WriteFrame(conn, FrameData, []byte("partial")); err != nil {
			return
		}
		// The agent dies before FrameEnd.
	})
	stream, err := client.OpenRead(context.Background(), "/tmp/x")
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	count, err := stream.Read(buffer)
	if err != nil || string(buffer[:count]) != "partial" {
		t.Fatalf("first read = %q, %v; want the data frame", buffer[:count], err)
	}
	if _, err := stream.Read(buffer); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read after the conn dropped = %v; want io.ErrUnexpectedEOF, never a clean io.EOF", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

func TestClientReadStreamEOFStaysAfterFrameEnd(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameOpenRead {
			return
		}
		if err := WriteMessage(conn, FrameOpened, OpenedMsg{Size: 4}); err != nil {
			return
		}
		if err := WriteFrame(conn, FrameData, []byte("data")); err != nil {
			return
		}
		if err := WriteFrame(conn, FrameEnd, nil); err != nil {
			return
		}
	})
	stream, err := client.OpenRead(context.Background(), "/tmp/x")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	buffer := make([]byte, 16)
	count, err := stream.Read(buffer)
	if err != nil || string(buffer[:count]) != "data" {
		t.Fatalf("read = %q, %v; want the data frame", buffer[:count], err)
	}
	for range 2 {
		if _, err := stream.Read(buffer); !errors.Is(err, io.EOF) {
			t.Fatalf("read after FrameEnd = %v; want io.EOF", err)
		}
	}
}

func TestClientDialStreamUnexpectedEOFBeforeDialEOF(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameDial {
			return
		}
		if err := WriteFrame(conn, FrameDialed, nil); err != nil {
			return
		}
		if err := WriteFrame(conn, FrameDialData, []byte("partial")); err != nil {
			return
		}
		// The proxied connection dies before FrameDialEOF.
	})
	stream, err := client.Dial(context.Background(), "tcp", "example:80")
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	count, err := stream.Read(buffer)
	if err != nil || string(buffer[:count]) != "partial" {
		t.Fatalf("first read = %q, %v; want the data frame", buffer[:count], err)
	}
	if _, err := stream.Read(buffer); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read after the conn dropped = %v; want io.ErrUnexpectedEOF, never a clean io.EOF", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

func TestClientUnaryUnexpectedEOFBeforeReply(t *testing.T) {
	for name, operation := range map[string]func(*Client) error{
		"Stat": func(client *Client) error {
			_, err := client.Stat(context.Background(), "/tmp/x")
			return err
		},
		"Roots": func(client *Client) error {
			_, err := client.Roots(context.Background())
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := pipeClient(t, func(conn net.Conn) {
				if _, _, err := ReadFrame(conn); err != nil {
					return
				}
				// The agent dies before any reply frame.
			})
			if err := operation(client); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("operation = %v; want io.ErrUnexpectedEOF, never a clean io.EOF", err)
			}
		})
	}
}

func TestClientWriteFileUnexpectedEOFBeforeReply(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameWriteFile {
			return
		}
		for {
			kind, _, err := ReadFrame(conn)
			if err != nil {
				return
			}
			if kind == FrameEnd {
				// The agent dies before the concluding FrameOK.
				return
			}
		}
	})
	err := client.WriteFile(context.Background(), "/tmp/x", []byte("payload"), false)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("WriteFile = %v; want io.ErrUnexpectedEOF, never a clean io.EOF", err)
	}
}

func TestClientWriteFileCancelMidStream(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), DataChunkSize+1) // two data frames
	gotHeader := make(chan struct{})
	serverStop := make(chan struct{})
	serverDone := make(chan struct{})
	var stopOnce sync.Once
	stopServer := func() { stopOnce.Do(func() { close(serverStop) }) }
	defer stopServer()
	client := pipeClient(t, func(conn net.Conn) {
		defer close(serverDone)
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameWriteFile {
			return
		}
		close(gotHeader)
		// Stop consuming: the client's next send blocks until the cancel
		// hook closes the conn, which the read after the stop observes.
		<-serverStop
		if _, _, err := ReadFrame(conn); err != nil {
			return
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.WriteFile(ctx, "/tmp/x", payload, false)
	}()
	select {
	case <-gotHeader:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the write header")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteFile = %v; want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteFile never returned after cancel")
	}
	stopServer()
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not close the conn; the server is still parked")
	}
}

func TestClientWriteStreamCancelAndClose(t *testing.T) {
	gotData := make(chan struct{})
	serverStop := make(chan struct{})
	client := pipeClient(t, func(conn net.Conn) {
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameOpenWrite {
			return
		}
		if err := WriteMessage(conn, FrameOpened, OpenedMsg{}); err != nil {
			return
		}
		// Take one data frame, then stop consuming entirely: the client's
		// next send blocks until the cancel hook closes the conn.
		if _, _, err := ReadFrame(conn); err != nil {
			return
		}
		close(gotData)
		<-serverStop
	})
	defer close(serverStop)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.OpenWrite(ctx, "/tmp/x", false)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("a"), DataChunkSize+1)
	type result struct {
		written int
		err     error
	}
	writeCh := make(chan result, 1)
	go func() {
		written, err := stream.Write(payload)
		writeCh <- result{written, err}
	}()
	select {
	case <-gotData:
	case <-time.After(5 * time.Second):
		t.Fatal("server never consumed the first data frame")
	}
	cancel()
	var write result
	select {
	case write = <-writeCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Write never returned after cancel")
	}
	if !errors.Is(write.err, context.Canceled) {
		t.Fatalf("Write = %v; want context.Canceled", write.err)
	}
	// The count covers only the frames accepted before the cancel; whether
	// the server applied them is unconfirmed, so it must not seed a replay.
	if write.written != DataChunkSize {
		t.Fatalf("Write reported %d bytes; want only the first chunk", write.written)
	}
	closeErr := make(chan error, 1)
	go func() {
		closeErr <- stream.Close()
	}()
	select {
	case err := <-closeErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Close = %v; want the cancellation evidence, not success", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung after the cancelled Write")
	}
	if err := stream.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Close = %v; want the same close evidence", err)
	}
}

func TestClientWriteStreamSyncUnexpectedEOF(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameOpenWrite {
			return
		}
		if err := WriteMessage(conn, FrameOpened, OpenedMsg{}); err != nil {
			return
		}
		for {
			kind, _, err := ReadFrame(conn)
			if err != nil {
				return
			}
			if kind == FrameSync {
				// The agent dies before the sync reply.
				return
			}
		}
	})
	stream, err := client.OpenWrite(context.Background(), "/tmp/x", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Sync(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Sync = %v; want io.ErrUnexpectedEOF, never a clean io.EOF", err)
	}
	if err := stream.Close(); err == nil {
		t.Fatal("Close must surface the terminal stream error")
	}
}
