package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListModelsPreservesOrderAndDuplicates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Method != http.MethodGet {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"data":[{"id":"m1"},{"id":7},{"name":"missing"},{"id":"m1"},{"id":"m2"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL + "/v1", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(models, []string{"m1", "m1", "m2"}) {
		t.Fatalf("models = %v", models)
	}
}

func TestListModelsMissingOrInvalidDataIsEmptySuccess(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":{}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(body))
			}))
			defer server.Close()
			client, _ := NewClient(Config{BaseURL: server.URL})
			models, err := client.ListModels(context.Background())
			if err != nil || len(models) != 0 {
				t.Fatalf("ListModels() = %v, %v", models, err)
			}
		})
	}
}

func TestTwoStepTestRunsChatAfterModelsFailure(t *testing.T) {
	var modelsCalls, chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/models":
			modelsCalls.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"error":{"message":"models unavailable"}}`))
		case "/chat/completions":
			chatCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if _, exists := body["stream"]; exists {
				t.Error("two-step test used a streaming chat request")
			}
			messages := body["messages"].([]any)
			if messages[0].(map[string]any)["content"] != "ping" {
				t.Errorf("test messages = %v", messages)
			}
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Model: "m"})
	result := client.Test(context.Background())
	if result.ModelsOK || result.ModelsError == nil || !strings.Contains(*result.ModelsError, "models unavailable") {
		t.Fatalf("models result = %+v", result)
	}
	if !result.ChatOK || result.ChatError != nil {
		t.Fatalf("chat result = %+v", result)
	}
	if modelsCalls.Load() != 1 || chatCalls.Load() != 1 {
		t.Fatalf("calls = models:%d chat:%d", modelsCalls.Load(), chatCalls.Load())
	}
	encoded, _ := json.Marshal(TestResult{ModelsOK: true, ChatOK: true})
	if string(encoded) != `{"modelsOk":true,"modelsError":null,"chatOk":true,"chatError":null}` {
		t.Fatalf("success JSON = %s", encoded)
	}
}

func TestTwoStepTestRejectsReasoningOnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/models" {
			_, _ = writer.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"thinking"}}]}`))
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Model: "m"})
	result := client.Test(context.Background())
	if !result.ModelsOK || result.ChatOK || result.ChatError == nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestExplicitProxyAndInvalidConfiguration(t *testing.T) {
	var proxied atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxied.Store(true)
		if !strings.HasPrefix(request.RequestURI, "http://example.invalid/v1/models") {
			t.Errorf("proxy RequestURI = %s", request.RequestURI)
		}
		_, _ = writer.Write([]byte(`{"data":[{"id":"through-proxy"}]}`))
	}))
	defer proxy.Close()
	client, err := NewClient(Config{BaseURL: "http://example.invalid/v1", Proxy: &proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(context.Background())
	if err != nil || !reflect.DeepEqual(models, []string{"through-proxy"}) || !proxied.Load() {
		t.Fatalf("proxy ListModels() = %v, %v, proxied=%v", models, err, proxied.Load())
	}
	invalidProxy := "://bad"
	if _, err := NewClient(Config{Proxy: &invalidProxy}); err == nil {
		t.Fatal("invalid proxy was accepted")
	}
	unsupportedProxy := "ftp://localhost:21"
	if _, err := NewClient(Config{Proxy: &unsupportedProxy}); err == nil {
		t.Fatal("unsupported proxy was accepted")
	}
	if _, err := NewClient(Config{APIKey: "bad\nkey"}); err == nil {
		t.Fatal("newline in API key was accepted")
	}
}

func TestModelListTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(time.Second):
			_, _ = writer.Write([]byte(`{"data":[]}`))
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL}, WithTimeouts(Timeouts{Models: 25 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.ListModels(context.Background())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("model timeout took %v", elapsed)
	}
}
