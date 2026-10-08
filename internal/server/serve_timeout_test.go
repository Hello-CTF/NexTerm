package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWithRequestTimeoutsCutsStalledBodyRead(t *testing.T) {
	const idle = 100 * time.Millisecond
	server := httptest.NewServer(withRequestTimeouts(idle, idle, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err == nil {
			t.Error("stalled body read must fail with idle timeout")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusRequestTimeout)
	})))
	defer server.Close()

	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := fmt.Fprintf(connection, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 100\r\n\r\na"); err != nil {
		t.Fatal(err)
	}
	// 只发 1 字节后停发, 超过 readIdle 后服务端读必须被判超时。
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusRequestTimeout)
	}
}

func TestWithRequestTimeoutsCutsBlockedWrite(t *testing.T) {
	const idle = 100 * time.Millisecond
	writeErr := make(chan error, 1)
	server := httptest.NewServer(withRequestTimeouts(idle, idle, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := make([]byte, 32<<10)
		for written := 0; written < 64<<20; {
			count, err := w.Write(payload)
			written += count
			if err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	})))
	defer server.Close()

	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := fmt.Fprintf(connection, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// 客户端一直不读, 服务端 socket 写阻塞超过 writeIdle 后必须被切断。
	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("blocked write must fail with idle timeout")
		}
		if networkErr, ok := err.(net.Error); !ok || !networkErr.Timeout() {
			t.Fatalf("write error = %v, want timeout", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked write was not cut")
	}
}

func TestWithRequestTimeoutsWebSocketSurvivesPastHeaderDeadline(t *testing.T) {
	const idle = 100 * time.Millisecond
	server := httptest.NewUnstartedServer(withRequestTimeouts(idle, idle, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "")
		messageType, data, err := connection.Read(context.Background())
		if err != nil {
			t.Errorf("websocket read long after handshake: %v", err)
			return
		}
		_ = connection.Write(context.Background(), messageType, append([]byte("echo:"), data...))
	})))
	// 与线上一致: 请求头读 deadline 由 ReadHeaderTimeout 设置, hijack 后必须被清除,
	// 否则长连接在握手后第一个 deadline 到期时就被误杀。
	server.Config.ReadHeaderTimeout = idle
	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws://"+server.Listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	// 空闲远超 idle 与 ReadHeaderTimeout 后再收发, 连接必须仍然存活。
	time.Sleep(6 * idle)
	if err := connection.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	_, data, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "echo:hi" {
		t.Fatalf("echo = %q, want %q", data, "echo:hi")
	}
}

func TestWithRequestTimeoutsNormalRequestUnaffected(t *testing.T) {
	server := httptest.NewServer(withRequestTimeouts(time.Minute, time.Minute, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write(append([]byte("echo:"), body...))
	})))
	defer server.Close()

	response, err := http.Post(server.URL, "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "echo:payload" {
		t.Fatalf("status = %d body = %q", response.StatusCode, body)
	}
}
