package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

type fakeDaemon struct {
	mu       sync.Mutex
	requests []string
}

func (d *fakeDaemon) record(request *http.Request) {
	d.mu.Lock()
	d.requests = append(d.requests, request.Method+" "+request.URL.Path+"?"+request.URL.RawQuery)
	d.mu.Unlock()
}

func (d *fakeDaemon) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	d.record(request)
	path := strings.TrimPrefix(request.URL.Path, "/v1.56")
	switch {
	case path == "/containers/json" && request.Method == http.MethodGet:
		writeTestJSON(writer, []container.Summary{{
			ID: "0123456789abcdef", Names: []string{"/web"}, Image: "nginx", State: container.StateRunning, Status: "Up",
		}})
	case path == "/images/json" && request.Method == http.MethodGet:
		writeTestJSON(writer, []image.Summary{{ID: "sha256:abcdef0123456789", RepoTags: []string{"app:v1"}, Size: 100, Created: 1}})
	case path == "/containers/c1/json" && request.Method == http.MethodGet:
		_, _ = io.WriteString(writer, `{"Id":"c1","Config":{"Tty":false},"FutureField":{"kept":true}}`)
	case path == "/containers/c1/stats" && request.Method == http.MethodGet:
		_, _ = io.WriteString(writer, `{"id":"c1","name":"/web","read":"2026-01-01T00:00:00Z","preread":"2026-01-01T00:00:00Z"}`)
	case path == "/containers/c1/logs" && request.Method == http.MethodGet:
		var output bytes.Buffer
		output.Write([]byte{byte(stdcopy.Stdout), 0, 0, 0, 0, 0, 0, 7})
		output.WriteString("stdout\n")
		output.Write([]byte{byte(stdcopy.Stderr), 0, 0, 0, 0, 0, 0, 7})
		output.WriteString("stderr\n")
		writer.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = writer.Write(output.Bytes())
	case path == "/images/create" && request.Method == http.MethodPost:
		_, _ = io.WriteString(writer, "{\"status\":\"Downloading\"}\n{\"status\":\"Done\"}\n")
	case strings.HasPrefix(path, "/images/") && request.Method == http.MethodDelete:
		_, _ = io.WriteString(writer, `[]`)
	case path == "/containers/c1/exec" && request.Method == http.MethodPost:
		writeTestJSON(writer, map[string]string{"Id": "e1"})
	case path == "/exec/e1/start" && request.Method == http.MethodPost:
		hijackTestExec(writer, request)
	case path == "/exec/e1/resize" && request.Method == http.MethodPost:
		writer.WriteHeader(http.StatusOK)
	case path == "/exec/e1/json" && request.Method == http.MethodGet:
		_, _ = io.WriteString(writer, `{"ID":"e1","ContainerID":"c1","Running":false,"ExitCode":0}`)
	case strings.HasPrefix(path, "/containers/c1/"):
		writer.WriteHeader(http.StatusNoContent)
	case path == "/containers/c1" && request.Method == http.MethodDelete:
		writer.WriteHeader(http.StatusNoContent)
	default:
		http.Error(writer, `{"message":"not found"}`, http.StatusNotFound)
	}
}

func writeTestJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func hijackTestExec(writer http.ResponseWriter, request *http.Request) {
	var options struct {
		Detach bool
		Tty    bool
	}
	if err := json.NewDecoder(request.Body).Decode(&options); err != nil || options.Detach {
		http.Error(writer, "invalid exec start", http.StatusBadRequest)
		return
	}
	connection, _, err := writer.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer connection.Close()
	contentType := "application/vnd.docker.raw-stream"
	if !options.Tty {
		contentType = "application/vnd.docker.multiplexed-stream"
	}
	_, _ = fmt.Fprintf(connection, "HTTP/1.1 101 UPGRADED\r\nContent-Type: %s\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n", contentType)
	if !options.Tty {
		payload := []byte(".\n..\n.env\nhello world.txt\nsub\n链接\n")
		header := make([]byte, 8)
		header[0] = byte(stdcopy.Stdout)
		binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
		_, _ = connection.Write(header)
		_, _ = connection.Write(payload)
		return
	}
	_, _ = io.WriteString(connection, "welcome\n")
	input := make([]byte, 4)
	if _, err := io.ReadFull(connection, input); err == nil && string(input) == "ping" {
		_, _ = io.WriteString(connection, "pong\n")
	}
}

func newFakeMobyBackend(t *testing.T) (*MobyBackend, *fakeDaemon) {
	t.Helper()
	daemon := &fakeDaemon{}
	server := httptest.NewServer(daemon)
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := client.New(
		client.WithHTTPClient(server.Client()),
		client.WithHost("tcp://"+serverURL.Host),
		client.WithAPIVersion("1.56"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cli.Close()
		server.Close()
	})
	return NewMobyBackend(cli), daemon
}

func TestMobyBackendReadsImagesInspectStatsAndFiles(t *testing.T) {
	backend, daemon := newFakeMobyBackend(t)
	containers, err := backend.ListContainers(t.Context(), true)
	if err != nil || len(containers) != 1 || containers[0].Names[0] != "/web" {
		t.Fatalf("containers = %+v, %v", containers, err)
	}
	images, err := backend.ListImages(t.Context())
	if err != nil || len(images) != 1 || images[0].RepoTags[0] != "app:v1" {
		t.Fatalf("images = %+v, %v", images, err)
	}
	inspect, err := backend.InspectContainer(t.Context(), "c1")
	if err != nil || !bytes.Contains(inspect, []byte("FutureField")) {
		t.Fatalf("inspect = %s, %v", inspect, err)
	}
	if _, err := backend.ContainerStats(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	logs, err := backend.OpenLogs(t.Context(), LogsOptions{Container: "c1", Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	output, err := readDemultiplexed(logs.Reader, logs.TTY)
	_ = logs.Reader.Close()
	if err != nil || output != "stdout\nstderr\n" {
		t.Fatalf("logs = %q, %v", output, err)
	}
	entries, err := backend.ListDir(t.Context(), "c1", "/var/lib/data")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", "..", ".env", "hello world.txt", "sub", "链接"}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %#v, want %#v", entries, want)
	}
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	for _, request := range daemon.requests {
		if strings.Contains(request, "/archive") {
			t.Fatalf("ListDir downloaded a recursive archive: %s", request)
		}
	}
}

func TestMobyBackendActionsAndPrivatePull(t *testing.T) {
	backend, daemon := newFakeMobyBackend(t)
	timeout := 7
	for _, action := range []Action{ActionStart, ActionStop, ActionRestart, ActionPause, ActionUnpause, ActionKill, ActionRemove, ActionRename} {
		if err := backend.Action(t.Context(), ActionOptions{Container: "c1", Action: action, NewName: "renamed", StopTimeout: &timeout}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if err := backend.RemoveImage(t.Context(), "app:v1", true); err != nil {
		t.Fatal(err)
	}
	output, err := backend.PullImage(t.Context(), PullOptions{Reference: "registry.local/team/app:v1", RegistryAuth: "encoded-auth"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(output, "\n") != 2 || !strings.Contains(output, "Done") {
		t.Fatalf("pull output = %q", output)
	}
	daemon.mu.Lock()
	requests := append([]string(nil), daemon.requests...)
	daemon.mu.Unlock()
	joined := strings.Join(requests, "\n")
	for _, expected := range []string{"t=7", "name=renamed", "force=1", "X-Registry-Auth"} {
		if expected == "X-Registry-Auth" {
			continue
		}
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %s in requests:\n%s", expected, joined)
		}
	}
}

func TestMobyBackendExecTTYResizeAndExit(t *testing.T) {
	backend, daemon := newFakeMobyBackend(t)
	session, err := backend.OpenExec(t.Context(), ExecOptions{
		Container: "c1", Cmd: []string{"sh"}, TTY: true, Width: 100, Height: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Resize(t.Context(), 120, 40); err != nil {
		t.Fatal(err)
	}
	readerDone := make(chan string, 1)
	go func() {
		output, _ := io.ReadAll(session)
		readerDone <- string(output)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := session.Write([]byte("ping")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case output := <-readerDone:
		if output != "welcome\npong\n" {
			t.Fatalf("exec output = %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for exec output")
	}
	exitCode, err := session.Wait(t.Context())
	if err != nil || exitCode != 0 {
		t.Fatalf("exit = %d, %v", exitCode, err)
	}
	_ = session.Close()
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	foundResize := false
	for _, request := range daemon.requests {
		if strings.Contains(request, "/exec/e1/resize") && strings.Contains(request, "h=40") && strings.Contains(request, "w=120") {
			foundResize = true
		}
	}
	if !foundResize {
		t.Fatalf("resize request not found: %v", daemon.requests)
	}
}

func TestMobyBackendPullReturnsStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "{\"status\":\"Downloading\"}\n{\"errorDetail\":{\"message\":\"denied\",\"code\":403},\"error\":\"denied\"}\n")
	}))
	defer server.Close()
	serverURL, _ := url.Parse(server.URL)
	cli, err := client.New(client.WithHost("tcp://"+serverURL.Host), client.WithHTTPClient(server.Client()), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	backend := NewMobyBackend(cli)
	_, err = backend.PullImage(context.Background(), PullOptions{Reference: "private.example/app:v1"})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("pull error = %v", err)
	}
}
