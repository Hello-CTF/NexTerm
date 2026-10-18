package vault

import (
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type partialRandomReader struct{}

func (partialRandomReader) Read(buffer []byte) (int, error) {
	copy(buffer, []byte{1, 2, 3})
	return 3, errors.New("random source unavailable")
}

func TestRandomSourceFailureClearsPartialOutput(t *testing.T) {
	buffer := make([]byte, DEKLength)
	err := readRandom(partialRandomReader{}, buffer)
	requireVaultCode(t, err, ipc.CodeCrypto)
	for _, value := range buffer {
		if value != 0 {
			t.Fatal("partial random bytes were retained after failure")
		}
	}
}
