package deviceproto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestFrameRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	message := RenameMsg{From: "/a", To: "/b"}
	if err := WriteMessage(&buffer, FrameRename, message); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := ReadFrame(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if kind != FrameRename {
		t.Fatalf("kind = %d; want FrameRename", kind)
	}
	var decoded RenameMsg
	if err := UnmarshalPayload(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != message {
		t.Fatalf("decoded = %+v; want %+v", decoded, message)
	}
}

func TestListenFrameValues(t *testing.T) {
	// The listen family continues the numbering after FrameDialEOF(27); the
	// values are a fixed wire contract with the client.
	if FrameListen != 28 || FrameListenAck != 29 || FrameInbound != 30 || FrameAccept != 31 {
		t.Fatalf("listen frames = %d/%d/%d/%d; want 28/29/30/31", FrameListen, FrameListenAck, FrameInbound, FrameAccept)
	}
}

func TestRootsFrameValues(t *testing.T) {
	if FrameRoots != 32 || FrameRootsReply != 33 {
		t.Fatalf("roots frames = %d/%d; want 32/33", FrameRoots, FrameRootsReply)
	}
}

func TestClientRootsRoundTrip(t *testing.T) {
	want := base.FileSystemRoots{
		PathStyle: base.PathStyleWindows,
		Roots: []base.FileRoot{
			{Path: `C:\`, Label: "C:"},
			{Path: `D:\`, Label: "D:"},
		},
		HomePath:  `C:\Users\agent`,
		Supported: true,
	}
	client := pipeClient(t, func(conn net.Conn) {
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameRoots {
			return
		}
		_ = WriteMessage(conn, FrameRootsReply, want)
	})
	got, err := client.Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.PathStyle != want.PathStyle || got.HomePath != want.HomePath || got.Supported != want.Supported || len(got.Roots) != len(want.Roots) {
		t.Fatalf("Roots = %+v; want %+v", got, want)
	}
	for index := range want.Roots {
		if got.Roots[index] != want.Roots[index] {
			t.Fatalf("root %d = %+v; want %+v", index, got.Roots[index], want.Roots[index])
		}
	}
}

func TestClientRootsErrors(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		code            string
		want            error
		wantUnsupported bool
	}{
		{name: "protocol rejection", code: CodeProtocol, want: ErrProtocol},
		{name: "explicit unsupported", code: CodeUnsupported, want: base.ErrUnsupported, wantUnsupported: true},
		{name: "enumeration failure", code: CodeInternal},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := pipeClient(t, func(conn net.Conn) {
				kind, _, err := ReadFrame(conn)
				if err != nil || kind != FrameRoots {
					return
				}
				_ = WriteMessage(conn, FrameError, ErrorMsg{Code: testCase.code, Message: "roots failure"})
			})
			roots, err := client.Roots(context.Background())
			if err == nil {
				t.Fatal("Roots must surface the agent error")
			}
			if testCase.want != nil && !errors.Is(err, testCase.want) {
				t.Fatalf("Roots error = %v; want errors.Is %v", err, testCase.want)
			}
			if errors.Is(err, base.ErrUnsupported) != testCase.wantUnsupported {
				t.Fatalf("Roots error = %v; unsupported = %v, want %v", err, errors.Is(err, base.ErrUnsupported), testCase.wantUnsupported)
			}
			if roots.Supported || len(roots.Roots) != 0 || roots.HomePath != "" {
				t.Fatalf("Roots = %+v after an error; want no fabricated roots", roots)
			}
		})
	}
}

func TestListenMessageRoundTrips(t *testing.T) {
	var buffer bytes.Buffer
	if err := WriteMessage(&buffer, FrameListen, ListenMsg{Network: "tcp", Address: "0.0.0.0:2222"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteMessage(&buffer, FrameListenAck, ListenAckMsg{Address: "10.0.0.1:2222"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteMessage(&buffer, FrameInbound, InboundMsg{ConnID: "conn-1", Address: "10.0.0.2:51234"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteMessage(&buffer, FrameAccept, AcceptMsg{ConnID: "conn-1"}); err != nil {
		t.Fatal(err)
	}

	kind, payload, err := ReadFrame(&buffer)
	if err != nil || kind != FrameListen {
		t.Fatalf("kind = %d, err = %v; want FrameListen", kind, err)
	}
	var listen ListenMsg
	if err := UnmarshalPayload(payload, &listen); err != nil {
		t.Fatal(err)
	}
	if listen != (ListenMsg{Network: "tcp", Address: "0.0.0.0:2222"}) {
		t.Fatalf("listen = %+v; want the written ListenMsg", listen)
	}

	kind, payload, err = ReadFrame(&buffer)
	if err != nil || kind != FrameListenAck {
		t.Fatalf("kind = %d, err = %v; want FrameListenAck", kind, err)
	}
	var ack ListenAckMsg
	if err := UnmarshalPayload(payload, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Address != "10.0.0.1:2222" {
		t.Fatalf("ack = %+v; want the bound address", ack)
	}

	kind, payload, err = ReadFrame(&buffer)
	if err != nil || kind != FrameInbound {
		t.Fatalf("kind = %d, err = %v; want FrameInbound", kind, err)
	}
	var inbound InboundMsg
	if err := UnmarshalPayload(payload, &inbound); err != nil {
		t.Fatal(err)
	}
	if inbound != (InboundMsg{ConnID: "conn-1", Address: "10.0.0.2:51234"}) {
		t.Fatalf("inbound = %+v; want the written InboundMsg", inbound)
	}

	kind, payload, err = ReadFrame(&buffer)
	if err != nil || kind != FrameAccept {
		t.Fatalf("kind = %d, err = %v; want FrameAccept", kind, err)
	}
	var accept AcceptMsg
	if err := UnmarshalPayload(payload, &accept); err != nil {
		t.Fatal(err)
	}
	if accept.ConnID != "conn-1" {
		t.Fatalf("accept = %+v; want the claimed ConnID", accept)
	}
}

func TestWriteFrameRejectsOversize(t *testing.T) {
	var buffer bytes.Buffer
	err := WriteFrame(&buffer, FrameData, make([]byte, MaxFramePayload+1))
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversize write = %v; want ErrProtocol", err)
	}
}

func TestReadFrameRejectsOversize(t *testing.T) {
	var buffer bytes.Buffer
	buffer.Write([]byte{0, 4, 0, 1, byte(FrameData)}) // MaxFramePayload+1 payload length
	_, _, err := ReadFrame(&buffer)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversize read = %v; want ErrProtocol", err)
	}
}

func TestErrorCodeRoundTrip(t *testing.T) {
	cases := []struct {
		err      error
		code     string
		sentinel error
	}{
		{fmt.Errorf("stat: %w", fs.ErrNotExist), CodeNotFound, fs.ErrNotExist},
		{fmt.Errorf("open: %w", fs.ErrPermission), CodePermission, fs.ErrPermission},
		{fmt.Errorf("mkdir: %w", fs.ErrExist), CodeExists, fs.ErrExist},
		{ErrInvalid, CodeInvalid, ErrInvalid},
		{ErrProtocol, CodeProtocol, ErrProtocol},
		{base.ErrUnsupported, CodeUnsupported, base.ErrUnsupported},
		{fmt.Errorf("list: %w", base.ErrNoMedia), CodeNoMedia, base.ErrNoMedia},
		{errors.New("boom"), CodeInternal, nil},
	}
	for _, tc := range cases {
		if code := ErrorCode(tc.err); code != tc.code {
			t.Fatalf("ErrorCode(%v) = %q; want %q", tc.err, code, tc.code)
		}
		mapped := CodedError(tc.code, "remote says no")
		if tc.sentinel != nil && !errors.Is(mapped, tc.sentinel) {
			t.Fatalf("CodedError(%q) = %v; want errors.Is %v", tc.code, mapped, tc.sentinel)
		}
	}
}

func TestWriteErrorReadErrorRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	if err := WriteError(&buffer, fmt.Errorf("stat /missing: %w", fs.ErrNotExist)); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := ReadFrame(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if kind != FrameError {
		t.Fatalf("kind = %d; want FrameError", kind)
	}
	err = ReadError(payload)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadError = %v; want fs.ErrNotExist", err)
	}
}

// pipeClient runs the scripted server half on one end of a net.Pipe and
// returns the client for the other.
func pipeClient(t *testing.T, serve func(net.Conn)) *Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		serve(serverConn)
	}()
	t.Cleanup(func() { clientConn.Close() })
	return NewClient(clientConn)
}

func TestClientWriteFileChunksAndConfirms(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 9000) // 72KiB, forces chunking
	var received []byte
	var header WriteMsg
	client := pipeClient(t, func(conn net.Conn) {
		kind, raw, err := ReadFrame(conn)
		if err != nil || kind != FrameWriteFile {
			return
		}
		if err := UnmarshalPayload(raw, &header); err != nil {
			return
		}
		for {
			kind, data, err := ReadFrame(conn)
			if err != nil {
				return
			}
			if kind == FrameData {
				received = append(received, data...)
				continue
			}
			if kind == FrameEnd {
				_ = WriteFrame(conn, FrameOK, nil)
			}
			return
		}
	})
	if err := client.WriteFile(context.Background(), "/tmp/x", payload, true); err != nil {
		t.Fatal(err)
	}
	if header.Path != "/tmp/x" || !header.Backup || header.Size != int64(len(payload)) {
		t.Fatalf("header = %+v; want path, backup, size %d", header, len(payload))
	}
	if !bytes.Equal(received, payload) {
		t.Fatalf("received %d bytes; want the %d-byte payload", len(received), len(payload))
	}
}

func TestClientWriteStreamAsyncError(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		kind, _, err := ReadFrame(conn)
		if err != nil || kind != FrameOpenWrite {
			return
		}
		if err := WriteMessage(conn, FrameOpened, OpenedMsg{}); err != nil {
			return
		}
		// Accept one data frame, then fail the stream asynchronously; keep
		// reading so the coded error, not a closed pipe, reaches the client.
		if _, _, err := ReadFrame(conn); err != nil {
			return
		}
		if err := WriteMessage(conn, FrameError, ErrorMsg{Code: CodeInternal, Message: "disk full"}); err != nil {
			return
		}
		for {
			if _, _, err := ReadFrame(conn); err != nil {
				return
			}
		}
	})
	stream, err := client.OpenWrite(context.Background(), "/tmp/x", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write(bytes.Repeat([]byte("a"), 100)); err != nil {
		t.Fatalf("first Write = %v; want success before the async failure", err)
	}
	if err := stream.Sync(); err == nil || !bytes.Contains([]byte(err.Error()), []byte("disk full")) {
		t.Fatalf("Sync = %v; want the async disk full error", err)
	}
	if err := stream.Close(); err == nil {
		t.Fatal("Close must surface the terminal stream error")
	}
}

func TestClientDialEchoAndHalfClose(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		kind, raw, err := ReadFrame(conn)
		if err != nil || kind != FrameDial {
			return
		}
		var msg DialMsg
		if err := UnmarshalPayload(raw, &msg); err != nil {
			return
		}
		if err := WriteFrame(conn, FrameDialed, nil); err != nil {
			return
		}
		for {
			kind, data, err := ReadFrame(conn)
			if err != nil {
				return
			}
			switch kind {
			case FrameDialData:
				if err := WriteFrame(conn, FrameDialData, data); err != nil {
					return
				}
			case FrameDialEOF:
				_ = WriteFrame(conn, FrameDialEOF, nil)
				return
			}
		}
	})
	stream, err := client.Dial(context.Background(), "tcp", "example:80")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(stream, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "ping" {
		t.Fatalf("echo = %q; want ping", buffer)
	}
	if err := stream.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write after CloseWrite = %v; want io.ErrClosedPipe", err)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read after peer EOF = %v; want io.EOF", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClientDialRejected(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		if _, _, err := ReadFrame(conn); err != nil {
			return
		}
		_ = WriteMessage(conn, FrameError, ErrorMsg{Code: CodeNotFound, Message: "no such service"})
	})
	_, err := client.Dial(context.Background(), "tcp", "127.0.0.1:1")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Dial = %v; want the coded fs.ErrNotExist", err)
	}
}

func TestClientUnexpectedReplyIsProtocolError(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		if _, _, err := ReadFrame(conn); err != nil {
			return
		}
		_ = WriteMessage(conn, FrameListed, ListChunk{})
	})
	_, err := client.Stat(context.Background(), "/tmp")
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("Stat = %v; want ErrProtocol for a mismatched reply", err)
	}
}
