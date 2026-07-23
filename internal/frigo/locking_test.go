package frigo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/roie/frigo/internal/lockfile"
)

func TestWorkspaceUsesTenSecondLockWaitAndAllowsShorterTests(t *testing.T) {
	ws, _ := newWorkspace(t)
	if ws.lockWait != 10*time.Second {
		t.Fatalf("lock wait = %v, want 10s", ws.lockWait)
	}

	guard, err := lockfile.Acquire(context.Background(), ws.repo.OperationLockPath, "test", 0)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() {
		if err := guard.Release(); err != nil {
			t.Errorf("Release() error = %v", err)
		}
	}()

	const wait = 35 * time.Millisecond
	ws.lockWait = wait
	started := time.Now()
	_, err = ws.List(context.Background(), nil)
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("List() error = %v, want lock timeout", err)
	}
	if elapsed < wait || elapsed > 500*time.Millisecond {
		t.Fatalf("List() waited %v, want %v to 500ms", elapsed, wait)
	}
}

func TestWithLockJoinsOperationAndReleaseFailures(t *testing.T) {
	ws, _ := newWorkspace(t)
	t.Cleanup(func() { _ = os.Remove(ws.repo.OperationLockPath) })
	operationErr := errors.New("operation failed")

	err := ws.withLock(context.Background(), "test", func() error {
		contents, err := os.ReadFile(ws.repo.OperationLockPath)
		if err != nil {
			return err
		}
		var metadata map[string]any
		if err := json.Unmarshal(contents, &metadata); err != nil {
			return err
		}
		metadata["token"] = strings.Repeat("0", 32)
		contents, err = json.Marshal(metadata)
		if err != nil {
			return err
		}
		if err := os.WriteFile(ws.repo.OperationLockPath, contents, 0o600); err != nil {
			return err
		}
		return operationErr
	})
	if !errors.Is(err, operationErr) {
		t.Fatalf("withLock() error = %v, want operation failure", err)
	}
	if !strings.Contains(err.Error(), "release operation lock") || !strings.Contains(err.Error(), "token") {
		t.Fatalf("withLock() error = %q, want joined release failure", err)
	}
}

func TestPublicOperationsAcquireLockBeforeExecuting(t *testing.T) {
	operations := []struct {
		name string
		run  func(*Workspace, context.Context) error
	}{
		{name: "add", run: func(ws *Workspace, ctx context.Context) error {
			_, err := ws.Add(ctx, []string{"PLAN.md"})
			return err
		}},
		{name: "commit", run: func(ws *Workspace, ctx context.Context) error {
			_, err := ws.Commit(ctx, CommitOptions{Message: "save", All: true})
			return err
		}},
		{name: "diff", run: func(ws *Workspace, ctx context.Context) error {
			_, err := ws.Diff(ctx, nil)
			return err
		}},
		{name: "list", run: func(ws *Workspace, ctx context.Context) error {
			_, err := ws.List(ctx, nil)
			return err
		}},
		{name: "log", run: func(ws *Workspace, ctx context.Context) error {
			_, err := ws.Log(ctx)
			return err
		}},
		{name: "release", run: func(ws *Workspace, ctx context.Context) error {
			_, err := ws.Release(ctx, []string{"PLAN.md"}, true)
			return err
		}},
		{name: "restore", run: func(ws *Workspace, ctx context.Context) error {
			_, err := ws.Restore(ctx, nil)
			return err
		}},
		{name: "status", run: func(ws *Workspace, ctx context.Context) error {
			_, err := ws.Status(ctx, nil)
			return err
		}},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			ws, _ := newWorkspace(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := operation.run(ws, ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("operation error = %v, want context.Canceled", err)
			}
			if !strings.Contains(err.Error(), "acquire operation lock") {
				t.Fatalf("operation error = %q, want lock acquisition failure", err)
			}
		})
	}
}
