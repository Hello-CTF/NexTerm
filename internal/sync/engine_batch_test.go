package sync

import (
	"bytes"
	"testing"
)

func TestTakePushBatchBoundaries(t *testing.T) {
	objects := []WireObject{
		{ID: "a", Blob: make([]byte, maxPushBatchBytes-10)},
		{ID: "b", Blob: make([]byte, 20)},
		{ID: "c", Blob: make([]byte, 10)},
	}
	hashes := []string{"ha", "hb", "hc"}

	batch, batchHashes, rest, restHashes := takePushBatch(objects, hashes)
	if len(batch) != 1 || batch[0].ID != "a" || len(batchHashes) != 1 || batchHashes[0] != "ha" {
		t.Fatalf("batch=%+v hashes=%v", batch, batchHashes)
	}
	if len(rest) != 2 || rest[0].ID != "b" || len(restHashes) != 2 || restHashes[0] != "hb" {
		t.Fatalf("rest=%+v hashes=%v", rest, restHashes)
	}

	batch, _, rest, _ = takePushBatch(rest, restHashes)
	if len(batch) != 2 || batch[0].ID != "b" || batch[1].ID != "c" || len(rest) != 0 {
		t.Fatalf("second batch=%+v rest=%+v", batch, rest)
	}

	// 单对象超过整批上限时独立成批, 不丢失。
	huge := []WireObject{{ID: "x", Blob: make([]byte, maxPushBatchBytes+1)}, {ID: "y", Blob: []byte("small")}}
	batch, _, rest, _ = takePushBatch(huge, []string{"hx", "hy"})
	if len(batch) != 1 || batch[0].ID != "x" || len(rest) != 1 || rest[0].ID != "y" {
		t.Fatalf("huge batch=%+v rest=%+v", batch, rest)
	}
	if !bytes.Equal(rest[0].Blob, []byte("small")) {
		t.Fatal("rest object mutated")
	}
}
