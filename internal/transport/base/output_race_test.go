package base

import (
	"context"
	"io"
	"sync"
	"testing"
)

func TestOutputRouterConcurrentRawCompletion(t *testing.T) {
	for index := range 200 {
		router := NewOutputRouter(context.Background())
		stdoutWriter := router.Writer(false)
		stderrWriter := router.Writer(true)
		var wait sync.WaitGroup
		results := make([]string, 2)
		errs := make([]error, 2)
		wait.Add(2)
		go func() {
			defer wait.Done()
			data, err := io.ReadAll(outputStdoutReader{router})
			results[0], errs[0] = string(data), err
		}()
		go func() {
			defer wait.Done()
			data, err := io.ReadAll(router.Stderr())
			results[1], errs[1] = string(data), err
		}()
		if index%2 == 0 {
			_, _ = stdoutWriter.Write([]byte("stdout"))
			_, _ = stderrWriter.Write([]byte("stderr"))
		} else {
			_, _ = stderrWriter.Write([]byte("stderr"))
			_, _ = stdoutWriter.Write([]byte("stdout"))
		}
		router.Close()
		wait.Wait()
		if errs[0] != nil || errs[1] != nil || results[0] != "stdout" || results[1] != "stderr" {
			router.mu.Lock()
			mode, out, stderr, ordered := router.mode, len(router.stdout.events), len(router.stderr.events), len(router.ordered.events)
			router.mu.Unlock()
			t.Fatalf("iteration %d: results = %q/%q, errors = %v/%v, mode = %d, queues = %d/%d/%d", index, results[0], results[1], errs[0], errs[1], mode, out, stderr, ordered)
		}
	}
}

type outputStdoutReader struct {
	router *OutputRouter
}

func (r outputStdoutReader) Read(data []byte) (int, error) {
	return r.router.ReadStdout(data)
}
