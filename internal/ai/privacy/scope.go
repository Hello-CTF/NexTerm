package privacy

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"sync"
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

// detectorIdentity lets a detector share findings across requests. Results are
// reused only for the same detector identity, scope key and exact text.
type detectorIdentity interface {
	detectionIdentity() string
}

func (d managedDetector) detectionIdentity() string {
	return fmt.Sprintf("%p\x00%s\x00%s\x00%s", d.manager, d.config.Revision, d.config.Precision, d.config.Version)
}

const detectionCacheEntries = 4096

type detectionCacheEntry struct {
	key   [32]byte
	spans []Span
}

// detectionCache bounds cross-request reuse of detector output so unchanged
// history is not re-inferred on every turn. It stores offsets and labels only.
type detectionCache struct {
	mu       sync.Mutex
	capacity int
	order    *list.List
	entries  map[[32]byte]*list.Element
}

func newDetectionCache(capacity int) *detectionCache {
	return &detectionCache{capacity: capacity, order: list.New(), entries: make(map[[32]byte]*list.Element)}
}

var sharedDetections = newDetectionCache(detectionCacheEntries)

func (c *detectionCache) get(key [32]byte) ([]Span, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return slices.Clone(element.Value.(*detectionCacheEntry).spans), true
}

func (c *detectionCache) put(key [32]byte, spans []Span) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		element.Value.(*detectionCacheEntry).spans = slices.Clone(spans)
		c.order.MoveToFront(element)
		return
	}
	c.entries[key] = c.order.PushFront(&detectionCacheEntry{key: key, spans: slices.Clone(spans)})
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*detectionCacheEntry).key)
	}
}

// detectionKey is a SHA-256 HMAC under the scope key, so cached findings are
// neither shared across conversations nor addressable from plain text alone.
func detectionKey(scope [32]byte, identity, text string) [32]byte {
	hash := hmac.New(sha256.New, scope[:])
	hash.Write([]byte("nexterm/privacy/detection/v1"))
	for _, value := range []string{identity, text} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		hash.Write(size[:])
		hash.Write([]byte(value))
	}
	var key [32]byte
	copy(key[:], hash.Sum(nil))
	return key
}
