package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGuardedConvertsPanicIntoFailureOutput(t *testing.T) {
	t.Parallel()
	output, err := Guarded(context.Background(), "list_assets", func() (Output, error) {
		panic("connection exploded")
	})
	if err != nil {
		t.Fatalf("panic leaked as error: %v", err)
	}
	if output.OK {
		t.Fatal("panic became a success output")
	}
	if !strings.Contains(output.Text, "list_assets") || !strings.Contains(output.Text, "connection exploded") {
		t.Fatalf("failure output lost panic context: %+v", output)
	}
	if output.ExitCode == 0 {
		t.Fatal("failure output must carry a non-zero exit code")
	}
}

func TestGuardedKeepsOrdinaryResults(t *testing.T) {
	t.Parallel()
	output, err := Guarded(context.Background(), "read_file", func() (Output, error) {
		return OK("fine"), nil
	})
	if err != nil || !output.OK || output.Text != "fine" {
		t.Fatalf("output=%+v err=%v", output, err)
	}
}

func TestGuardedDoesNotSwallowCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := Guarded(ctx, "wait_for", func() (Output, error) {
		<-ctx.Done()
		panic("late panic")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation swallowed into tool output: output=%+v err=%v", output, err)
	}
	if output.OK || output.Text != "" {
		t.Fatalf("canceled call produced output: %+v", output)
	}
}
