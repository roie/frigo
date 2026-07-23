//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestCommandContextCanceledByTerminationSignal(t *testing.T) {
	ctx, cancel := commandContext()
	defer cancel()

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal() error = %v", err)
	}

	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error = %v, want context.Canceled", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("command context was not canceled")
	}
}
