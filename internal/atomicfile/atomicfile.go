package atomicfile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Write atomically replaces filename with data, creating parent directories as needed.
func Write(filename string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".frigo-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tempName, filename); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}
