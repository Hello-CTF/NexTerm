package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// startIdleTestServer runs Application.Serve on a real listener with a short read idle timeout.
func startIdleTestServer(t *testing.T, readIdle time.Duration) string {
	t.Helper()
	application, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	transport := http.NewServeMux()
	transport.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestTimeout)
			return
		}
		_, _ = w.Write(append([]byte("echo:"), body...))
	})
	transport.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusRequestTimeout)
			return
		}
		select {
		case <-r.Context().Done():
			http.Error(w, "request context canceled: "+r.Context().Err().Error(), http.StatusServiceUnavailable)
		case <-time.After(4 * readIdle):
			_, _ = w.Write([]byte("done"))
		}
	})
	transport.HandleFunc("/write-then-work", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("early"))
		select {
		case <-r.Context().Done():
		case <-time.After(4 * readIdle):
		}
	})
	transport.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "")
		messageType, data, err := connection.Read(r.Context())
		if err != nil {
			return
		}
		_ = connection.Write(r.Context(), messageType, append([]byte("echo:"), data...))
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- application.Serve(ctx, ServeConfig{
			Listen: "127.0.0.1:0", Listener: listener, Transport: transport,
			ReadIdleTimeout: readIdle, WriteIdleTimeout: readIdle, Stderr: io.Discard,
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-result:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	})
	return listener.Addr().String()
}

func TestServeCutsStalledRequestBody(t *testing.T) {
	address := startIdleTestServer(t, 200*time.Millisecond)

	stalled, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	if _, err := fmt.Fprintf(stalled, "POST /echo HTTP/1.1\r\nHost: test\r\nContent-Length: 1000\r\n\r\npartial"); err != nil {
		t.Fatal(err)
	}

	// A normal request on the same server is unaffected while the other connection is stalled.
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post("http://"+address+"/echo", "text/plain", strings.NewReader("ok"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "echo:ok" {
		t.Fatalf("normal request = %d %q", response.StatusCode, body)
	}

	// The server must finish the stalled exchange (error response) and close the connection.
	_ = stalled.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, bufio.NewReader(stalled)); err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			t.Fatal("stalled connection was not closed by the server")
		}
	}
}

func TestServeKeepsIdleWebSocketAlive(t *testing.T) {
	const idle = 200 * time.Millisecond
	address := startIdleTestServer(t, idle)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws://"+address+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	time.Sleep(4 * idle)
	if err := connection.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	_, data, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "echo:hi" {
		t.Fatalf("echo = %q", data)
	}
}

func TestServeLongHandlerAfterBodyKeepsRequestContext(t *testing.T) {
	address := startIdleTestServer(t, 200*time.Millisecond)
	client := &http.Client{Timeout: 10 * time.Second}
	for name, send := range map[string]func() (*http.Response, error){
		"post": func() (*http.Response, error) {
			return client.Post("http://"+address+"/slow", "text/plain", strings.NewReader("payload"))
		},
		"get": func() (*http.Response, error) { return client.Get("http://" + address + "/slow") },
	} {
		response, err := send()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || string(body) != "done" {
			t.Fatalf("%s: status = %d body = %q", name, response.StatusCode, body)
		}
	}
}

func TestServeBufferedWriteThenLongWorkStillDelivers(t *testing.T) {
	address := startIdleTestServer(t, 200*time.Millisecond)
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get("http://" + address + "/write-then-work")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "early" {
		t.Fatalf("body = %q err = %v", body, err)
	}
}

func TestServeBoundsTransportClose(t *testing.T) {
	previous := transportCloseTimeout
	transportCloseTimeout = 100 * time.Millisecond
	t.Cleanup(func() { transportCloseTimeout = previous })
	application, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- application.Serve(ctx, ServeConfig{
			Listen: "127.0.0.1:0", Listener: listener, Transport: http.NewServeMux(), Stderr: io.Discard,
			// A transport whose sockets never finish closing must not hold shutdown forever.
			CloseTransport: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		})
	}()
	// Wait until Serve is accepting requests so the shutdown path, not startup, handles the cancellation.
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Serve error = %v, want the transport close deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the transport close deadline")
	}
}
