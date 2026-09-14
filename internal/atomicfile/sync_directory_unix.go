//go:build !windows

package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open containing directory: %w", err)
	}
	return syncDirectoryHandle(file)
}

func syncDirectoryHandle(file interface {
	Sync() error
	Close() error
}) error {
	syncErr := file.Sync()
	// Some Unix filesystems do not implement directory fsync. Only these errors
	// from fsync itself mean unsupported; open, close and genuine I/O errors fail.
	if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) {
		syncErr = nil
	}
	return errors.Join(wrapDirectoryError("sync", syncErr), wrapDirectoryError("close", file.Close()))
}

func wrapDirectoryError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s containing directory: %w", operation, err)
}
