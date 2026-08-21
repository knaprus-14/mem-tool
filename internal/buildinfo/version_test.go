package buildinfo

import (
	"regexp"
	"testing"
)

func TestVersionIsCanonicalSemanticVersion(t *testing.T) {
	pattern := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	if !pattern.MatchString(Version) {
		t.Fatalf("Version = %q, want canonical SemVer without v prefix", Version)
	}
}
