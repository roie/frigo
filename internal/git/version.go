package git

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a semantic Git version reduced to its numeric major, minor, and patch parts.
type Version struct {
	Major int
	Minor int
	Patch int
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// AtLeast reports whether v is greater than or equal to minimum.
func (v Version) AtLeast(minimum Version) bool {
	if v.Major != minimum.Major {
		return v.Major > minimum.Major
	}
	if v.Minor != minimum.Minor {
		return v.Minor > minimum.Minor
	}
	return v.Patch >= minimum.Patch
}

var versionPattern = regexp.MustCompile(`(?i)^git version\s+(\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion parses output from git --version, including vendor suffixes.
func ParseVersion(output string) (Version, error) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return Version{}, fmt.Errorf("cannot parse Git version from %q", strings.TrimSpace(output))
	}

	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch := 0
	if match[3] != "" {
		patch, _ = strconv.Atoi(match[3])
	}
	return Version{Major: major, Minor: minor, Patch: patch}, nil
}

// CheckMinimum verifies that the configured Git executable satisfies minimum.
func CheckMinimum(ctx context.Context, client Client, minimum Version) error {
	output, err := client.Output(ctx, "", "--version")
	if err != nil {
		return fmt.Errorf("Git is required: %w", err)
	}
	version, err := ParseVersion(output)
	if err != nil {
		return err
	}
	if !version.AtLeast(minimum) {
		return fmt.Errorf("Git %s or newer is required; found %s", minimum, version)
	}
	return nil
}
