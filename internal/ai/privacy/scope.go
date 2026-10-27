package privacy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

type conversationContextKey struct{}

// WithConversation must be called only after the caller has authorized access
// to the conversation. Account identity is resolved independently by the manager.
func WithConversation(ctx context.Context, conversationID string) context.Context {
	return context.WithValue(ctx, conversationContextKey{}, conversationID)
}

func conversationFromContext(ctx context.Context) string {
	return ConversationID(ctx)
}

// ConversationID returns the conversation scope attached after authorization.
func ConversationID(ctx context.Context) string {
	id, _ := ctx.Value(conversationContextKey{}).(string)
	return id
}

func deriveScopeKey(master [32]byte, ownerID, conversationID string) [32]byte {
	hash := hmac.New(sha256.New, master[:])
	hash.Write([]byte("nexterm-ai-privacy-v1"))
	for _, value := range []string{ownerID, conversationID} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		hash.Write(size[:])
		hash.Write([]byte(value))
	}
	var key [32]byte
	copy(key[:], hash.Sum(nil))
	return key
}
