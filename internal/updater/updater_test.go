package updater

import (
	"os"
	"testing"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "updater-test-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func TestContainsValidCIDR(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{"valid single range", "192.168.0.0/16", true},
		{"valid with comments", "# comment\n10.0.0.0/8", true},
		{"valid with blank lines", "\n\n172.16.0.0/12\n\n", true},
		{"only comments", "# just a comment\n# another", false},
		{"only blanks", "\n\n  \n", false},
		{"invalid CIDR", "not-a-cidr\nbad.stuff", false},
		{"empty file", "", false},
		{"mixed invalid and valid", "bad\n196.168.0.0/24", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, tt.content)
			result := containsValidCIDR(path)
			if result != tt.expected {
				t.Errorf("containsValidCIDR = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestContainsValidCIDRMissingFile(t *testing.T) {
	if containsValidCIDR("/nonexistent/file.txt") {
		t.Error("expected false for missing file")
	}
}
