package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/roie/frigo/internal/atomicfile"
)

const CurrentVersion = 1

var ErrUnsupportedVersion = errors.New("unsupported registry version")

// Registry records the root-relative paths actively owned by frigo.
type Registry struct {
	Version int      `json:"version"`
	Paths   []string `json:"paths"`
}

type AddResult struct {
	Added           []string
	AlreadyOwned    map[string]string
	ReleasedCovered []string
}

type ReleaseResult struct {
	Released []string
	Missing  []string
}

func New() Registry {
	return Registry{Version: CurrentVersion, Paths: []string{}}
}

func Load(filename string) (Registry, error) {
	file, err := os.Open(filename)
	if err != nil {
		return Registry{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var owned Registry
	if err := decoder.Decode(&owned); err != nil {
		return Registry{}, fmt.Errorf("decode registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Registry{}, fmt.Errorf("decode registry: trailing JSON value")
		}
		return Registry{}, fmt.Errorf("decode registry: trailing data: %w", err)
	}
	if err := owned.validate(); err != nil {
		return Registry{}, err
	}
	owned.Paths = normalize(owned.Paths)
	if owned.Paths == nil {
		owned.Paths = []string{}
	}
	return owned, nil
}

func Save(filename string, owned Registry) error {
	if owned.Version == 0 {
		owned.Version = CurrentVersion
	}
	if err := owned.validate(); err != nil {
		return err
	}
	owned.Paths = normalize(owned.Paths)
	if owned.Paths == nil {
		owned.Paths = []string{}
	}

	data, err := json.MarshalIndent(owned, "", "  ")
	if err != nil {
		return fmt.Errorf("encode registry: %w", err)
	}
	data = append(data, '\n')
	return atomicfile.Write(filename, data, 0o600)
}

func (r *Registry) Add(paths ...string) (AddResult, error) {
	if err := validatePaths(paths); err != nil {
		return AddResult{}, fmt.Errorf("invalid add paths: %w", err)
	}

	next := Registry{Version: r.Version, Paths: append([]string(nil), r.Paths...)}
	if next.Version == 0 {
		next.Version = CurrentVersion
	}
	next.Paths = normalize(next.Paths)
	result := AddResult{AlreadyOwned: make(map[string]string)}

	for _, candidate := range paths {
		if covering, ok := coveringPath(next.Paths, candidate); ok {
			result.AlreadyOwned[candidate] = covering
			continue
		}

		kept := next.Paths[:0]
		for _, existing := range next.Paths {
			if covers(candidate, existing) {
				result.ReleasedCovered = append(result.ReleasedCovered, existing)
				continue
			}
			kept = append(kept, existing)
		}
		next.Paths = append(kept, candidate)
		result.Added = append(result.Added, candidate)
	}

	next.Paths = normalize(next.Paths)
	sort.Strings(result.ReleasedCovered)
	*r = next
	return result, nil
}

func (r *Registry) Release(paths ...string) (ReleaseResult, error) {
	if err := validatePaths(paths); err != nil {
		return ReleaseResult{}, fmt.Errorf("invalid release paths: %w", err)
	}

	next := Registry{Version: r.Version, Paths: append([]string(nil), r.Paths...)}
	next.Paths = normalize(next.Paths)
	removeSet := make(map[string]struct{}, len(paths))
	for _, candidate := range paths {
		removeSet[candidate] = struct{}{}
	}

	result := ReleaseResult{}
	kept := make([]string, 0, len(next.Paths))
	for _, existing := range next.Paths {
		if _, ok := removeSet[existing]; ok {
			result.Released = append(result.Released, existing)
			delete(removeSet, existing)
			continue
		}
		kept = append(kept, existing)
	}
	for _, candidate := range paths {
		if _, ok := removeSet[candidate]; ok {
			result.Missing = append(result.Missing, candidate)
			delete(removeSet, candidate)
		}
	}
	next.Paths = kept
	*r = next
	return result, nil
}

func (r Registry) Owns(candidate string) bool {
	_, ok := coveringPath(r.Paths, candidate)
	return ok
}

func (r Registry) OwnsExact(candidate string) bool {
	for _, existing := range r.Paths {
		if existing == candidate {
			return true
		}
	}
	return false
}
