//go:build !unix

package applog

import (
	"os"
	"testing"
)

func journalStreamOf(*testing.T, *os.File) string { return "" }
