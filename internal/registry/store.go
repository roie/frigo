package registry

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

func (r Registry) validate() error {
	if r.Version != CurrentVersion {
		return fmt.Errorf("%w: found %d, want %d", ErrUnsupportedVersion, r.Version, CurrentVersion)
	}
	if err := validatePaths(r.Paths); err != nil {
		return fmt.Errorf("invalid registry: %w", err)
	}
	return nil
}

func coveringPath(paths []string, candidate string) (string, bool) {
	for _, existing := range paths {
		if covers(existing, candidate) {
			return existing, true
		}
	}
	return "", false
}

func covers(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+"/")
}

func normalize(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	for _, candidate := range paths {
		candidate = strings.TrimPrefix(path.Clean(candidate), "./")
		if candidate == "" || candidate == "." {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		normalized = append(normalized, candidate)
	}
	sort.Strings(normalized)
	return normalized
}

func validatePaths(paths []string) error {
	for _, candidate := range paths {
		if err := validatePath(candidate); err != nil {
			return err
		}
	}
	return nil
}

func validatePath(candidate string) error {
	if candidate == "" || candidate == "." || strings.HasPrefix(candidate, "/") {
		return fmt.Errorf("invalid path %q", candidate)
	}
	if strings.ContainsAny(candidate, "\r\n") {
		return fmt.Errorf("path contains a newline: %q", candidate)
	}
	clean := path.Clean(candidate)
	if clean != candidate || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("invalid path %q", candidate)
	}
	return nil
}
