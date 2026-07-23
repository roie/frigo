package lockfile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAcquireExclusivelyAndReportsOwner(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "frigo.lock")
	lock, err := Acquire(context.Background(), filename, "add", 0)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var got owner
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatalf("lock JSON error = %v", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}
	if got.Hostname != hostname || got.PID != os.Getpid() || got.Operation != "add" {
		t.Fatalf("owner = %+v, want hostname %q, pid %d, operation add", got, hostname, os.Getpid())
	}
	if got.StartedAt.Location() != time.UTC {
		t.Fatalf("owner start location = %v, want UTC", got.StartedAt.Location())
	}
	if len(got.Token) != 32 {
		t.Fatalf("token length = %d, want 32", len(got.Token))
	}
	if _, err := strconv.ParseUint(got.Token[:16], 16, 64); err != nil {
		t.Fatalf("token is not lowercase hex: %q", got.Token)
	}
	if got.Token != strings.ToLower(got.Token) {
		t.Fatalf("token = %q, want lowercase", got.Token)
	}

	_, err = Acquire(context.Background(), filename, "release", 35*time.Millisecond)
	if err == nil {
		t.Fatal("second Acquire() error = nil, want contention error")
	}
	for _, want := range []string{"add", hostname, strconv.Itoa(os.Getpid())} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("second Acquire() error = %q, want owner detail %q", err, want)
		}
	}
}

func TestAcquireWaitsOnlyForConfiguredBudget(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "frigo.lock")
	first, err := Acquire(context.Background(), filename, "add", 0)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() {
		if err := first.Release(); err != nil {
			t.Errorf("Release() error = %v", err)
		}
	}()

	const wait = 40 * time.Millisecond
	started := time.Now()
	_, err = Acquire(context.Background(), filename, "status", wait)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("contended Acquire() error = nil")
	}
	if elapsed < wait {
		t.Fatalf("Acquire() waited %v, want at least %v", elapsed, wait)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Acquire() waited %v, want bounded wait", elapsed)
	}
}

func TestAcquireStopsWhenContextIsCanceled(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "frigo.lock")
	first, err := Acquire(context.Background(), filename, "add", 0)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() { _ = first.Release() }()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := Acquire(ctx, filename, "status", 10*time.Second)
		result <- err
	}()
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire() did not stop after cancellation")
	}
}

func TestReleaseRefusesChangedToken(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "frigo.lock")
	lock, err := Acquire(context.Background(), filename, "add", 0)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var replacement owner
	if err := json.Unmarshal(contents, &replacement); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	replacement.Token = strings.Repeat("0", 32)
	contents, err = json.Marshal(replacement)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filename, contents, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := lock.Release(); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("Release() error = %v, want token mismatch", err)
	}
	if _, err := os.Stat(filename); err != nil {
		t.Fatalf("changed lock was removed: %v", err)
	}
}

func TestAcquireNeverStealsPreexistingFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "frigo.lock")
	const contents = "not a frigo lock"
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := Acquire(context.Background(), filename, "add", 25*time.Millisecond)
	if err == nil {
		t.Fatal("Acquire() error = nil, want contention error")
	}
	got, readErr := os.ReadFile(filename)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(got) != contents {
		t.Fatalf("pre-existing contents = %q, want %q", got, contents)
	}
}
