package diff

import (
	"strings"
	"testing"
)

// expectContains asserts that list contains target (exact match on any element).
func expectContains(t *testing.T, list []string, target string) {
	t.Helper()
	for _, item := range list {
		if item == target {
			return
		}
	}
	t.Fatalf("expected list to contain %q; got %v", target, strings.Join(list, "; "))
}
