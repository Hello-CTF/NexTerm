package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestNativeGenerateHonorsOverrideToolsFallbackAndMetadata(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusTooManyRequests), blockStep("served-backup", "ok")}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup"}, 0, nil, nil)
	base, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chatModel, err := base.WithTools([]*schema.ToolInfo{{Name: "write", Desc: "write a file"}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := chatModel.Generate(context.Background(), []*schema.Message{{Role: schema.RoleType("user"), Content: "hi"}}, model.WithModel("override"))
	if err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if !reflect.DeepEqual(requestModels(requests), []string{"override", "backup"}) || requests[1].toolCount != 1 {
		t.Fatalf("native requests = %+v", requests)
	}
	if message.Content != "ok" {
		t.Fatalf("native message = %+v", message)
	}
	metadata := MetadataFromMessage(message)
	if metadata.RunID == "" || metadata.CallID == "" || metadata.Model != "served-backup" || metadata.ContextWindow != 1000 {
		t.Fatalf("native metadata = %+v", metadata)
	}
}

func TestNativeStreamHonorsOverrideToolsFallbackAndMetadata(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{errorStep(http.StatusInternalServerError), streamStep("served-backup", "stream-ok")}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup", Stream: true}, 0, nil, nil)
	base, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chatModel, err := base.WithTools([]*schema.ToolInfo{{Name: "write"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := chatModel.Stream(context.Background(), []*schema.Message{{Role: schema.RoleType("user"), Content: "hi"}}, model.WithModel("override"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var messages []*schema.Message
	for {
		message, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if len(messages) == 0 || messages[0].Content != "stream-ok" {
		t.Fatalf("native stream messages = %+v", messages)
	}
	metadata := MetadataFromMessage(messages[0])
	for _, message := range messages[1:] {
		if got := MetadataFromMessage(message); got != metadata {
			t.Fatalf("frame metadata = %+v, want %+v", got, metadata)
		}
	}
	if metadata.RunID == "" || metadata.CallID == "" || metadata.Model != "served-backup" || metadata.ContextWindow != 1000 {
		t.Fatalf("stream metadata = %+v", metadata)
	}
	requests := provider.Requests()
	if !reflect.DeepEqual(requestModels(requests), []string{"override", "backup"}) || !requests[1].stream || requests[1].toolCount != 1 {
		t.Fatalf("native stream requests = %+v", requests)
	}
}

func TestNativeStreamDoesNotReplayAfterFirstFrame(t *testing.T) {
	provider := &fakeProvider{steps: []fakeStep{{
		status: http.StatusOK,
		body:   "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: invalid\n\n",
	}, streamStep("backup", "must-not-run")}}
	client := newFakeClient(t, provider, Config{Model: "primary", FallbackModel: "backup", Stream: true}, 3, nil, nil)
	chatModel, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := chatModel.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var contents []string
	for {
		message, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			t.Fatal("stream ended without the provider error")
		}
		if recvErr != nil {
			break
		}
		contents = append(contents, message.Content)
	}
	if !reflect.DeepEqual(contents, []string{"partial"}) {
		t.Fatalf("replayed native frames = %v", contents)
	}
	if got := requestModels(provider.Requests()); !reflect.DeepEqual(got, []string{"primary"}) {
		t.Fatalf("post-frame native requests = %v", got)
	}
}
