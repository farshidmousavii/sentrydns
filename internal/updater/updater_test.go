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

func makeCIDRs(cidrs ...string) string {
	var out string
	for _, c := range cidrs {
		out += c + "\n"
	}
	return out
}

func TestContainsValidCIDR(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{"valid enough ranges",
			makeCIDRs(
				"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12",
				"1.1.1.0/24", "2.2.2.0/24", "3.3.3.0/24",
				"4.4.4.0/24", "5.5.5.0/24", "6.6.6.0/24",
				"7.7.7.0/24",
			), true},
		{"valid with comments",
			"# comment\n" + makeCIDRs(
				"10.0.0.0/8", "172.16.0.0/12",
				"1.1.1.0/24", "2.2.2.0/24", "3.3.3.0/24",
				"4.4.4.0/24", "5.5.5.0/24", "6.6.6.0/24",
				"7.7.7.0/24", "8.8.8.0/24",
			), true},
		{"valid with blank lines",
			"\n\n" + makeCIDRs(
				"172.16.0.0/12",
				"1.1.1.0/24", "2.2.2.0/24", "3.3.3.0/24",
				"4.4.4.0/24", "5.5.5.0/24", "6.6.6.0/24",
				"7.7.7.0/24", "8.8.8.0/24", "9.9.9.0/24",
			) + "\n\n", true},
		{"too few valid", "192.168.0.0/16\n10.0.0.0/8", false},
		{"only comments", "# just a comment\n# another", false},
		{"only blanks", "\n\n  \n", false},
		{"invalid CIDR", "not-a-cidr\nbad.stuff", false},
		{"empty file", "", false},
		{"mixed too few valid",
			"bad\n" + makeCIDRs(
				"196.168.0.0/24", "10.0.0.0/8", "172.16.0.0/12",
				"1.1.1.0/24", "2.2.2.0/24", "3.3.3.0/24",
			), false},
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
