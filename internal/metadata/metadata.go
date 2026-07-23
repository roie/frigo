package metadata

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/roie/frigo/internal/atomicfile"
)

const CurrentVersion = 1

var ErrUnsupportedVersion = errors.New("unsupported manifest version")

// Manifest stores frigo's stable metadata for a worktree.
type Manifest struct {
	Version      int    `json:"version"`
	ID           string `json:"id"`
	WorktreePath string `json:"worktreePath"`
	LockOwned    bool   `json:"lockOwned"`
}

// GenerateID returns a 32-character lowercase hexadecimal identifier.
func GenerateID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// Load reads and validates a manifest from filename.
func Load(filename string) (Manifest, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Manifest{}, err
	}
	if !utf8.Valid(data) {
		return Manifest{}, fmt.Errorf("manifest is not valid UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("decode manifest: trailing JSON value")
		}
		return Manifest{}, fmt.Errorf("decode manifest: trailing data: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Save writes a manifest to filename atomically.
func Save(filename string, manifest Manifest) error {
	if manifest.Version == 0 {
		manifest.Version = CurrentVersion
	}
	if err := manifest.validate(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	data = append(data, '\n')
	return atomicfile.Write(filename, data, 0o600)
}

// LoadPointer reads a stable frigo pointer file.
func LoadPointer(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("pointer is not valid UTF-8")
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return "", fmt.Errorf("decode pointer: missing terminal newline")
	}

	id := string(data[:len(data)-1])
	if err := validateID(id); err != nil {
		return "", err
	}
	return id, nil
}

// SavePointer writes a pointer file containing the ID and one terminal newline.
func SavePointer(filename, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	return atomicfile.Write(filename, append([]byte(id), '\n'), 0o600)
}

func (m Manifest) validate() error {
	if m.Version != CurrentVersion {
		return ErrUnsupportedVersion
	}
	if err := validateID(m.ID); err != nil {
		return fmt.Errorf("invalid manifest id: %w", err)
	}
	if !utf8.ValidString(m.WorktreePath) {
		return fmt.Errorf("manifest worktreePath is not valid UTF-8")
	}
	return nil
}

func validateID(id string) error {
	if len(id) != 32 {
		return fmt.Errorf("invalid id length")
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return fmt.Errorf("invalid id")
		}
	}
	return nil
}
