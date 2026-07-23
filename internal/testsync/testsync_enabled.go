//go:build frigo_test

package testsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	syncDirEnv = "FRIGO_TEST_SYNC_DIR"
	pauseEnv   = "FRIGO_TEST_PAUSE"
	failEnv    = "FRIGO_TEST_FAIL"
)

var inducedFailures sync.Map

// Point reports that the process reached name and, when configured, waits for
// the parent test to create the matching continuation file.
func Point(ctx context.Context, name string) error {
	if err := Notify(name); err != nil {
		return err
	}
	if !listed(os.Getenv(pauseEnv), name) {
		return nil
	}

	gate := eventPath("continue", name)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(gate); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect test synchronization gate: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Notify reports a process event without waiting.
func Notify(name string) error {
	if os.Getenv(syncDirEnv) == "" {
		return nil
	}
	if err := os.WriteFile(eventPath("event", name), nil, 0o600); err != nil {
		return fmt.Errorf("write test synchronization event: %w", err)
	}
	return nil
}

// Fail returns one configured failure for name in the current process.
func Fail(name string) error {
	if !listed(os.Getenv(failEnv), name) {
		return nil
	}
	if _, loaded := inducedFailures.LoadOrStore(name, struct{}{}); loaded {
		return nil
	}
	return fmt.Errorf("induced test failure at %s", name)
}

func eventPath(kind, name string) string {
	return filepath.Join(os.Getenv(syncDirEnv), fmt.Sprintf("%s.%d.%s", kind, os.Getpid(), name))
}

func listed(values, want string) bool {
	for _, value := range strings.Split(values, ",") {
		if value == want {
			return true
		}
	}
	return false
}
