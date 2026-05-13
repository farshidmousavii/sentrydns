package classifier

import "testing"

func TestClassifier(t *testing.T) {
	c, err := New("../../data/iran-ranges.txt")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		ip       string
		expected bool
	}{
		{"5.22.0.1", true},
		{"2.144.0.1", true},
		{"8.8.8.8", false},
		{"142.250.0.1", false},
		{"5.160.211.1", true},
	}

	for _, tt := range tests {
		result := c.IsIran(tt.ip)
		if result != tt.expected {
			t.Errorf("IsIran(%s) = %v, want %v", tt.ip, result, tt.expected)
		}
	}
}
