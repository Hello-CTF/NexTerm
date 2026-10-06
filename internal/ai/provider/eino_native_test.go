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
	if extraString(message.Extra, MessageExtraRunID) == "" || extraString(message.Extra, MessageExtraCallID) == "" ||
		extraString(message.Extra, MessageExtraServingModel) != "served-backup" || extraUint64(message.Extra, MessageExtraContextWindow) != 1000 {
		t.Fatalf("native metadata = %+v", message.Extra)
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
	runID := extraString(messages[0].Extra, MessageExtraRunID)
	callID := extraString(messages[0].Extra, MessageExtraCallID)
	servingModel := extraString(messages[0].Extra, MessageExtraServingModel)
	contextWindow := extraUint64(messages[0].Extra, MessageExtraContextWindow)
	for _, message := range messages[1:] {
		if extraString(message.Extra, MessageExtraRunID) != runID || extraString(message.Extra, MessageExtraCallID) != callID ||
			extraString(message.Extra, MessageExtraServingModel) != servingModel || extraUint64(message.Extra, MessageExtraContextWindow) != contextWindow {
			t.Fatalf("frame metadata = %+v, want %s/%s/%s/%d", message.Extra, runID, callID, servingModel, contextWindow)
		}
	}
	if runID == "" || callID == "" || servingModel != "served-backup" || contextWindow != 1000 {
		t.Fatalf("stream metadata = %s/%s/%s/%d", runID, callID, servingModel, contextWindow)
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
