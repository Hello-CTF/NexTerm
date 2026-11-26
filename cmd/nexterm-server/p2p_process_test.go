package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/pion/stun/v4"
)

func TestServerProcessRejectsInvalidP2PEnvironment(t *testing.T) {
	binary := buildServerBinary(t)
	for _, entry := range []string{"NEXTERM_P2P=sometimes", "NEXTERM_STUN_LISTEN=no-port", "NEXTERM_STUN_ADVERTISE=nx.example.com", "NEXTERM_STUN_EXTERNAL=stun.example.net:0", "NEXTERM_RELAY_QUIC=sometimes", "NEXTERM_RELAY_QUIC_LISTEN=no-port", "NEXTERM_RELAY_QUIC_ADVERTISE=nx.example.com"} {
		t.Run(entry, func(t *testing.T) {
			cmd := exec.Command(binary, "--listen", "127.0.0.1:0", "--data-dir", t.TempDir())
			cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=", entry)
			output, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("%s = %v, output %q", entry, err, output)
			}
			name, _, _ := strings.Cut(entry, "=")
			if !strings.Contains(string(output), name) {
				t.Fatalf("error does not name %s: %q", name, output)
			}
		})
	}
}

func TestServerProcessAnswersSTUNOnTheConfiguredAddress(t *testing.T) {
	binary := buildServerBinary(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stunAddress := probe.LocalAddr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	dataDir := t.TempDir()
	cmd := exec.Command(binary, "--listen", address, "--data-dir", dataDir)
	cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=", "NEXTERM_STUN_LISTEN="+stunAddress)
	buffer := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = buffer, buffer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &testServerProcess{cmd: cmd, wait: make(chan error, 1), output: buffer}
	go func() { process.wait <- cmd.Wait() }()
	defer process.stop(t)

	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	target, err := net.ResolveUDPAddr("udp", stunAddress)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	buffer2 := make([]byte, 1500)
	for {
		if err := process.err(); err != nil {
			t.Fatalf("server exited: %v\n%s", err, buffer.String())
		}
		request, err := stun.Build(stun.TransactionID, stun.BindingRequest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.WriteTo(request.Raw, target); err != nil {
			t.Fatal(err)
		}
		_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := client.ReadFrom(buffer2)
		if err == nil {
			response := &stun.Message{Raw: append([]byte(nil), buffer2[:n]...)}
			var mapped stun.XORMappedAddress
			if err := response.Decode(); err != nil || response.Type != stun.BindingSuccess || mapped.GetFrom(response) != nil {
				t.Fatalf("response = %v, %v", response, err)
			}
			if mapped.Port != client.LocalAddr().(*net.UDPAddr).Port {
				t.Fatalf("mapped = %s, client = %s", mapped.String(), client.LocalAddr())
			}
			checkProcessRelay(t, "http://"+address, stunAddress, dataDir, buffer)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no STUN response: %v\n%s", err, buffer.String())
		}
	}
}

// checkProcessRelay checks the relay of a running server: the QUIC carrier answers on the STUN port with the
// key kept in the data directory, and the shared mux serves the relay routes as fleet JSON, not the web app.
func checkProcessRelay(t *testing.T, base, stunAddress, dataDir string, output *syncBuffer) {
	t.Helper()
	cert, err := relaymux.LoadOrCreatePinnedCertificate(filepath.Join(dataDir, fleetserver.RelayQUICKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := relaymux.DialQUIC(ctx, stunAddress, relaymux.DialConfig{Pin: cert.Pin})
	if err != nil {
		t.Fatalf("QUIC relay on the STUN port: %v\n%s", err, output.String())
	}
	_ = session.Close()

	var response *http.Response
	for {
		response, err = http.Get(base + relaymux.ConfigPath)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("relay config: %v\n%s", err, output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer response.Body.Close()
	var body struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if response.StatusCode != http.StatusUnauthorized || !strings.Contains(response.Header.Get("Content-Type"), "application/json") ||
		json.NewDecoder(response.Body).Decode(&body) != nil || body.Error.Code != "forbidden" {
		t.Fatalf("anonymous relay config = HTTP %d %s %+v", response.StatusCode, response.Header.Get("Content-Type"), body)
	}

	_, err = relaymux.Dial(ctx, relaymux.DialConfig{
		WSURL: "ws" + strings.TrimPrefix(base, "http") + relaymux.WSPath,
		Hello: relaymux.Hello{Role: relaymux.RoleClient, Token: "not-a-session"},
	})
	if reply, ok := relaymux.Refused(err); !ok || reply.Status != http.StatusUnauthorized {
		t.Fatalf("relay websocket with a bad token = %v", err)
	}
}
