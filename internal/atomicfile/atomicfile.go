// Package atomicfile publishes synced file contents, then syncs the containing
// directory where supported. It does not sync newly created ancestor directories
// or promise a transaction spanning multiple files.
package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/roie/frigo/internal/testsync"
)

// PublishedError means the destination was published, but subsequent temporary
// cleanup or directory sync failed. Callers must not treat it as a failed publication.
type PublishedError struct {
	Path string
	Err  error
}

func (e *PublishedError) Error() string {
	return fmt.Sprintf("published %s but post-publication housekeeping failed: %v", e.Path, e.Err)
}
func (e *PublishedError) Unwrap() error { return e.Err }

// IsPublishedError reports whether err contains only post-publication housekeeping
// failures. A joined operational failure must still trigger normal compensation.
func IsPublishedError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*PublishedError); ok {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !IsPublishedError(child) {
				return false
			}
		}
		return true
	}
	return IsPublishedError(errors.Unwrap(err))
}

// Write atomically replaces filename with data, creating parent directories as needed.
func Write(filename string, data []byte, mode fs.FileMode) error {
	return write(filename, data, mode, os.Rename, syncDirectory, os.Remove)
}

// Create publishes filename without replacing an existing destination. Like Write,
// a PublishedError means the final name exists, not just a temporary file.
func Create(filename string, data []byte, mode fs.FileMode) error {
	return write(filename, data, mode, os.Link, syncDirectory, os.Remove)
}

func write(filename string, data []byte, mode fs.FileMode, publish func(string, string) error, syncDir func(string) error, removeTemp func(string) error) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".frigo-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempName := temp.Name()
	published := false
	defer func() {
		if !published {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := testsync.Fail("atomic-file-sync-" + filepath.Base(filename)); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := testsync.Fail("atomic-before-publish-" + filepath.Base(filename)); err != nil {
		return err
	}
	if err := publish(tempName, filename); err != nil {
		return fmt.Errorf("publish file: %w", err)
	}
	published = true
	cleanupErr := testsync.Fail("atomic-cleanup-" + filepath.Base(filename))
	if cleanupErr == nil {
		cleanupErr = removeTemp(tempName)
	}
	if os.IsNotExist(cleanupErr) {
		cleanupErr = nil
	}
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("remove temporary file %s: %w", tempName, cleanupErr)
	}
	err = testsync.Fail("atomic-directory-sync-" + filepath.Base(filename))
	if err == nil {
		err = syncDir(dir)
	}
	if err != nil {
		err = fmt.Errorf("confirm directory durability: %w", err)
	}
	if err = errors.Join(cleanupErr, err); err != nil {
		return &PublishedError{Path: filename, Err: err}
	}
	return nil
}
