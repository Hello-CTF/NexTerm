//go:build !unix

package applog

import "os"

func stderrIsJournal(string, *os.File) bool { return false }
