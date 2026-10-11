package version

import (
	"testing"
)

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		remote   string
		current  string
		expected bool
	}{
		{"v2.2.9", "v2.2.8", true},
		{"v2.3.0", "v2.2.8", true},
		{"v3.0.0", "v2.2.8", true},
		{"v2.2.8", "v2.2.8", false},
		{"v2.2.7", "v2.2.8", false},
		{"2.2.9", "v2.2.8", true},
		{"v2.2.8.1", "v2.2.8", true},
		{"", "v2.2.8", false},
		{"v2.2.8", "", false},
	}

	for _, tc := range tests {
		actual := IsNewerVersion(tc.remote, tc.current)
		if actual != tc.expected {
			t.Errorf("IsNewerVersion(%q, %q) = %v; want %v", tc.remote, tc.current, actual, tc.expected)
		}
	}
}
