package agent

import "context"

type CheckpointStore interface {
	CheckpointGet(ctx context.Context, id string) ([]byte, bool, error)
	CheckpointSet(ctx context.Context, id string, data []byte) error
	CheckpointDelete(ctx context.Context, id string) error
}

type storeCheckpoints struct {
	store CheckpointStore
}

func NewStoreCheckpoints(store CheckpointStore) *storeCheckpoints {
	return &storeCheckpoints{store: store}
}

func (s *storeCheckpoints) Get(ctx context.Context, id string) ([]byte, bool, error) {
	return s.store.CheckpointGet(ctx, id)
}

func (s *storeCheckpoints) Set(ctx context.Context, id string, value []byte) error {
	return s.store.CheckpointSet(ctx, id, value)
}

func (s *storeCheckpoints) Delete(ctx context.Context, id string) error {
	return s.store.CheckpointDelete(ctx, id)
}
