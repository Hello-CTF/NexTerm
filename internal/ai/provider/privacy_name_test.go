package provider

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy"
)

// Message.Name is serialized by the OpenAI adapter, so it must go through the same protection as content.
func TestProtectNativeMessagesProtectsName(t *testing.T) {
	exchange, err := privacy.NewScopedExchange(privateValues("token-123"), [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	input := []*schema.Message{{Role: schema.User, Name: "user-token-123", Content: "hi"}}
	output, err := protectNativeMessages(t.Context(), exchange, input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output[0].Name, "token-123") || output[0].Name == "" {
		t.Fatalf("name = %q, want protected placeholder text", output[0].Name)
	}
	if input[0].Name != "user-token-123" {
		t.Fatal("caller message mutated")
	}
}
