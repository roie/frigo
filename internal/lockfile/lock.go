// Package lockfile serializes operations that share repository metadata.
package lockfile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

const pollInterval = 100 * time.Millisecond

type owner struct {
	Hostname  string    `json:"hostname"`
	PID       int       `json:"pid"`
	Operation string    `json:"operation"`
	StartedAt time.Time `json:"started_at"`
	Token     string    `json:"token"`
}

// Lock is an exclusively acquired lock file.
type Lock struct {
	filename string
	token    string
}

// Acquire creates filename exclusively, waiting up to wait for an existing
// owner to release it. The caller must release the returned lock.
func Acquire(ctx context.Context, filename, operation string, wait time.Duration) (*Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("acquire operation lock %s: %w", filename, err)
		}

		lock, err := tryAcquire(filename, operation)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire operation lock %s: %w", filename, err)
		}

		ownerDescription := describeOwner(filename)
		remaining := time.Until(deadline)
		if wait <= 0 || remaining <= 0 {
			return nil, fmt.Errorf("acquire operation lock %s: timed out waiting for %s", filename, ownerDescription)
		}
		delay := min(pollInterval, remaining)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("acquire operation lock %s: %w", filename, ctx.Err())
		case <-timer.C:
		}
	}
}

func tryAcquire(filename, operation string) (*Lock, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate owner token: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("read hostname: %w", err)
	}
	metadata := owner{
		Hostname:  hostname,
		PID:       os.Getpid(),
		Operation: operation,
		StartedAt: time.Now().UTC(),
		Token:     hex.EncodeToString(tokenBytes),
	}

	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		_ = file.Close()
		_ = os.Remove(filename)
		return nil, fmt.Errorf("write owner metadata: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(filename)
		return nil, fmt.Errorf("close owner metadata: %w", err)
	}
	return &Lock{filename: filename, token: metadata.Token}, nil
}

func describeOwner(filename string) string {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Sprintf("existing owner (details unavailable: %v)", err)
	}
	var metadata owner
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return fmt.Sprintf("existing owner (invalid metadata: %v)", err)
	}
	return fmt.Sprintf("operation %q by pid %d on %s since %s", metadata.Operation, metadata.PID, metadata.Hostname, metadata.StartedAt.Format(time.RFC3339Nano))
}

// Release removes the lock only if the on-disk owner token still matches.
func (l *Lock) Release() error {
	contents, err := os.ReadFile(l.filename)
	if err != nil {
		return fmt.Errorf("read operation lock %s: %w", l.filename, err)
	}
	var metadata owner
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return fmt.Errorf("read operation lock owner %s: %w", l.filename, err)
	}
	if metadata.Token != l.token {
		return fmt.Errorf("release operation lock %s: owner token changed", l.filename)
	}
	if err := os.Remove(l.filename); err != nil {
		return fmt.Errorf("remove operation lock %s: %w", l.filename, err)
	}
	return nil
}
