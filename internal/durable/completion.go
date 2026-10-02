package durable

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func (b *Backend) recorderComplete(id string) (bool, error) {
	data, err := os.ReadFile(b.recorderDonePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: read recorder completion: %v", ErrUnavailable, err)
	}
	status := strings.TrimSpace(string(data))
	if status == "0" {
		return true, nil
	}
	return false, fmt.Errorf("%w: recorder exited with status %s", ErrUnavailable, status)
}
