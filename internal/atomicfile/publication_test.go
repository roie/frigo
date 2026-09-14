package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPublicationPhases(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		for _, phase := range []string{"success", "publish", "cleanup", "sync", "cleanup-and-sync"} {
			t.Run(fmt.Sprintf("exclusive=%v/%s", exclusive, phase), func(t *testing.T) {
				filename := filepath.Join(t.TempDir(), "metadata")
				if !exclusive {
					if err := os.WriteFile(filename, []byte("old"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				publishErr := errors.New("publication failed")
				cleanupErr := errors.New("cleanup failed")
				syncErr := errors.New("directory IO failed")
				var events []string
				var temporary string
				publish := func(temp, dest string) error {
					events = append(events, "publish")
					temporary = temp
					assertFileContents(t, temp, "new")
					if phase == "publish" {
						return publishErr
					}
					if exclusive {
						return os.Link(temp, dest)
					}
					return os.Rename(temp, dest)
				}
				cleanup := func(temp string) error {
					events = append(events, "cleanup")
					assertFileContents(t, filename, "new")
					if phase == "cleanup" || phase == "cleanup-and-sync" {
						return cleanupErr
					}
					return os.Remove(temp)
				}
				sync := func(dir string) error {
					events = append(events, "sync")
					if dir != filepath.Dir(filename) {
						t.Fatalf("sync dir=%q", dir)
					}
					assertFileContents(t, filename, "new")
					if phase == "sync" || phase == "cleanup-and-sync" {
						return syncErr
					}
					return nil
				}
				err := write(filename, []byte("new"), 0600, publish, sync, cleanup)
				if phase == "publish" {
					if !errors.Is(err, publishErr) || IsPublishedError(err) {
						t.Fatalf("pre-publication error=%v", err)
					}
					if !slices.Equal(events, []string{"publish"}) {
						t.Fatal(events)
					}
					if exclusive {
						if _, statErr := os.Stat(filename); !os.IsNotExist(statErr) {
							t.Fatalf("destination exists: %v", statErr)
						}
					} else {
						assertFileContents(t, filename, "old")
					}
				} else {
					if !slices.Equal(events, []string{"publish", "cleanup", "sync"}) {
						t.Fatal(events)
					}
					assertFileContents(t, filename, "new")
					if (err != nil) != (phase != "success") || IsPublishedError(err) != (phase != "success") {
						t.Fatalf("error=%v", err)
					}
					var published *PublishedError
					if err != nil && (!errors.As(err, &published) || published.Path != filename) {
						t.Fatalf("lost destination: %v", err)
					}
					if errors.Is(err, cleanupErr) != (phase == "cleanup" || phase == "cleanup-and-sync") {
						t.Fatalf("cleanup cause: %v", err)
					}
					if errors.Is(err, syncErr) != (phase == "sync" || phase == "cleanup-and-sync") {
						t.Fatalf("sync cause: %v", err)
					}
					if phase == "cleanup" && strings.Contains(err.Error(), "durability") {
						t.Fatalf("cleanup misreported as sync error: %v", err)
					}
				}
				if phase != "cleanup" && phase != "cleanup-and-sync" {
					if _, statErr := os.Stat(temporary); !os.IsNotExist(statErr) {
						t.Fatalf("temporary remains: %v", statErr)
					}
				}
			})
		}
	}
}

func TestCreateNeverReplacesDestination(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "metadata")
	if err := Create(filename, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	err := Create(filename, []byte("second"), 0600)
	if err == nil || IsPublishedError(err) {
		t.Fatalf("Create error=%v", err)
	}
	assertFileContents(t, filename, "first")
	entries, err := os.ReadDir(filepath.Dir(filename))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: %v %v", entries, err)
	}
}

func TestIsPublishedErrorRejectsMixedFailures(t *testing.T) {
	cause := errors.New("IO")
	published := &PublishedError{Path: "metadata", Err: cause}
	wrapped := fmt.Errorf("save: %w", published)
	if !IsPublishedError(wrapped) || !errors.Is(wrapped, cause) {
		t.Fatal(wrapped)
	}
	if !IsPublishedError(errors.Join(wrapped, published)) {
		t.Fatal("lost joined published errors")
	}
	for _, err := range []error{nil, cause, errors.Join(wrapped, cause), fmt.Errorf("outer: %w", errors.Join(wrapped, cause))} {
		if IsPublishedError(err) {
			t.Fatalf("misclassified operational error: %v", err)
		}
	}
}

func assertFileContents(t *testing.T, filename, want string) {
	t.Helper()
	got, err := os.ReadFile(filename)
	if err != nil || string(got) != want {
		t.Fatalf("%s=%q,%v want %q", filename, got, err, want)
	}
}
