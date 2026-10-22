package production

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/logtail"
	gossh "golang.org/x/crypto/ssh"
)

type logTailSSHFixture struct {
	listener net.Listener
	config   *gossh.ServerConfig
	commands chan string
	closed   chan struct{}
	ptyCalls atomic.Int32
}

func newLogTailSSHFixture(t *testing.T) *logTailSSHFixture {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &gossh.ServerConfig{
		PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			if metadata.User() == "test" && string(password) == "secret" {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid test credentials")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &logTailSSHFixture{
		listener: listener, config: config,
		commands: make(chan string, 1), closed: make(chan struct{}, 1),
	}
	go fixture.accept()
	t.Cleanup(func() { _ = listener.Close() })
	return fixture
}

func (f *logTailSSHFixture) port() int {
	return f.listener.Addr().(*net.TCPAddr).Port
}

func (f *logTailSSHFixture) accept() {
	for {
		connection, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handleConnection(connection)
	}
}

func (f *logTailSSHFixture) handleConnection(connection net.Conn) {
	serverConnection, channels, requests, err := gossh.NewServerConn(connection, f.config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer serverConnection.Close()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(gossh.UnknownChannelType, "log tail fixture")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err == nil {
			go f.handleSession(channel, channelRequests)
		}
	}
}

func (f *logTailSSHFixture) handleSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "pty-req":
			f.ptyCalls.Add(1)
			_ = request.Reply(true, nil)
		case "exec":
			var payload struct{ Command string }
			if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
				_ = request.Reply(false, nil)
				continue
			}
			f.commands <- payload.Command
			_ = request.Reply(true, nil)
			_, _ = channel.Write([]byte("stdout-line\n"))
			_, _ = channel.Stderr().Write([]byte("stderr-line\n"))
			_, _ = channel.Read(make([]byte, 1))
			f.closed <- struct{}{}
			return
		default:
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
		}
	}
}

func TestLogTailProductionSSHStreamsWithoutPTYAndCancelClosesRemote(t *testing.T) {
	fixture := newLogTailSSHFixture(t)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credentialID := createConnectorTestCredential(t, production, "log-tail-password", "secret")
	assetID := createConnectorTestAsset(t, production, "log-tail-target", "127.0.0.1", fixture.port(), "password", credentialID, `{"autoAcceptUnknownHost":true}`)
	connected := connectHostKeyTestAsset(t, production, assetID)
	environment := production.Environment("client-log-tail")
	environment.Streams = hub.StreamFactory{Hub: production.Services.Sessions.Hub()}
	channelID := "log-tail-ssh-stream"
	input := fmt.Sprintf(`{"runId":"ssh-log-tail","sources":[{"sourceId":"source","sessionId":%q,"kind":"file","value":"/var/log/app.log"}],"maxConcurrent":1}`, connected.ID)
	response := production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "log_tail_start", Args: json.RawMessage(input), Channel: ipc.ChannelRef{ID: channelID}, ClientID: "client-log-tail",
	}, environment)
	if !response.OK {
		t.Fatalf("log tail start = %+v", response)
	}
	receiver, err := production.Services.Sessions.Hub().Bind(channelID)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	lines := make(map[string]bool)
	deadline := time.After(3 * time.Second)
	for len(lines) < 2 {
		var frame hub.Frame
		select {
		case <-deadline:
			t.Fatal("timed out waiting for real SSH log lines")
		default:
		}
		frameCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		frame, err = receiver.Next(frameCtx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
		var event logtail.Event
		if err := json.Unmarshal(frame.Data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Kind == logtail.EventLine {
			lines[event.Text] = true
			if event.SourceID != "source" || event.SessionID != connected.ID {
				t.Fatalf("misattributed SSH line: %+v", event)
			}
		}
	}
	if !lines["stdout-line"] || !lines["stderr-line"] {
		t.Fatalf("SSH lines = %+v", lines)
	}
	select {
	case command := <-fixture.commands:
		if command != "tail -n 200 -f -- '/var/log/app.log'" {
			t.Fatalf("SSH command = %q", command)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSH fixture did not receive exec")
	}
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "log_tail_cancel", Args: json.RawMessage(`{"runId":"ssh-log-tail"}`), ClientID: "client-log-tail",
	}, environment)
	if !response.OK {
		t.Fatalf("log tail cancel = %+v", response)
	}
	select {
	case <-fixture.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("remote SSH exec channel remained open after cancellation")
	}
	if got := fixture.ptyCalls.Load(); got != 0 {
		t.Fatalf("log tail issued %d pty-req requests", got)
	}
}
