//go:build !windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestSyncDirectoryErrorPolicy(t *testing.T) {
	for _, syncErr := range []error{nil, syscall.EINVAL, syscall.ENOTSUP, syscall.EIO, syscall.EACCES, syscall.EBADF} {
		for _, closeErr := range []error{nil, syscall.EINVAL, syscall.EIO} {
			file := &directoryHandle{syncErr: syncErr, closeErr: closeErr}
			err := syncDirectoryHandle(file)
			unsupported := errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP)
			wantError := (syncErr != nil && !unsupported) || closeErr != nil
			if (err != nil) != wantError {
				t.Fatalf("sync=%v close=%v error=%v", syncErr, closeErr, err)
			}
			if syncErr != nil && !unsupported && !errors.Is(err, syncErr) {
				t.Fatalf("lost sync error: %v", err)
			}
			if closeErr != nil && !errors.Is(err, closeErr) {
				t.Fatalf("lost close error: %v", err)
			}
			if !slices.Equal(file.events, []string{"sync", "close"}) {
				t.Fatal(file.events)
			}
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := syncDirectory(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open error=%v", err)
	}
	if err := syncDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

type directoryHandle struct {
	syncErr, closeErr error
	events            []string
}

func (f *directoryHandle) Sync() error  { f.events = append(f.events, "sync"); return f.syncErr }
func (f *directoryHandle) Close() error { f.events = append(f.events, "close"); return f.closeErr }
