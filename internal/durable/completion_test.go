package durable

import (
	"bytes"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestCompletionFilterHandlesFragmentedMarker(t *testing.T) {
	id := ids.New()
	filter := newCompletionFilter(id)
	marker := completionMarker(id)
	input := append([]byte("before"), marker...)
	var output []byte
	for index, value := range input {
		filter.append([]byte{value})
		chunk := make([]byte, 3)
		for {
			count, complete := filter.read(chunk)
			output = append(output, chunk[:count]...)
			if complete {
				if index != len(input)-1 {
					t.Fatalf("completion ended at input byte %d of %d", index, len(input))
				}
			}
			if count == 0 {
				break
			}
		}
	}
	if !bytes.Equal(output, []byte("before")) {
		t.Fatalf("filtered output = %q", output)
	}
	if count, complete := filter.read(make([]byte, 1)); count != 0 || !complete {
		t.Fatalf("final filter read = %d, %v", count, complete)
	}
}

func TestCompletionFilterFinishFlushesMarkerPrefix(t *testing.T) {
	id := ids.New()
	filter := newCompletionFilter(id)
	marker := completionMarker(id)
	input := append([]byte("user-"), marker[:11]...)
	filter.append(input)
	filter.finish()
	output := make([]byte, len(input))
	count, complete := filter.read(output)
	if count != len(input) || complete || !bytes.Equal(output, input) {
		t.Fatalf("finish data read = %d, %v, %q", count, complete, output[:count])
	}
	count, complete = filter.read(make([]byte, 1))
	if count != 0 || !complete {
		t.Fatalf("finish EOF read = %d, %v", count, complete)
	}
}
